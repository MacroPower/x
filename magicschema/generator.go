package magicschema

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/magicschema/internal/yamldoc"
)

// Sentinel errors returned by the generator.
var (
	ErrInvalidYAML   = errors.New("invalid yaml")
	ErrInvalidOption = errors.New("invalid option")
	ErrReadInput     = errors.New("read input")
	ErrMarshalSchema = errors.New("marshal schema")
	ErrWriteOutput   = errors.New("write output")
)

// Generator produces JSON Schema from YAML input.
type Generator struct {
	title       string
	description string
	id          string
	annotators  []Annotator

	// Marker recognizers of the prepared annotators, set on the per-document
	// copy of the Generator alongside annotators. The fallback description
	// extractor consults them (see [Generator.isAnnotationLine]) so a custom
	// annotator's marker lines never leak into descriptions.
	markers []MarkerAnnotator

	strict        bool
	inferDefaults bool

	// Walk recursion depth on the per-document copy of the Generator,
	// bounding alias cycles and nesting deeper than maxWalkDepth.
	depth int

	// Walked node count on the per-document copy of the Generator,
	// bounding total work when aliases fan out exponentially (chained
	// anchors each referenced multiple times, billion-laughs style).
	visits int

	// Items-schema nesting level on the per-document copy of the
	// Generator. An items schema describes every element of a sequence,
	// so a default lifted from one observed element would be arbitrary;
	// default recording is suppressed while non-zero.
	inItems int

	// Walked node count for default inference (astToGoValue), kept separate
	// from visits so re-walking a subtree to record its default never
	// deducts from the structural walk's budget. Sharing one counter let
	// WithInferDefaults exhaust the budget mid-document and fail later
	// properties' structural schemas open that inferDefaults=off would emit.
	defaultVisits int
}

// maxWalkDepth bounds schema-walk recursion, counted once per container
// nesting level (mapping or sequence items). YAML alias cycles (an anchor
// whose subtree aliases back to itself) would otherwise recurse forever;
// every such cycle passes through a container walker, and subtrees past
// the bound fail open to the empty schema.
const maxWalkDepth = 1000

// maxNodeVisits bounds the total nodes walked per document. The depth bound
// alone cannot stop exponential alias fan-out, which grows breadth rather
// than depth; subtrees past the budget fail open to the empty schema.
const maxNodeVisits = 500_000

// Option configures a Generator.
type Option func(*Generator)

// NewGenerator creates a Generator with the given options.
func NewGenerator(opts ...Option) *Generator {
	g := &Generator{}

	for _, opt := range opts {
		opt(g)
	}

	return g
}

// WithAnnotators sets the annotators to use, in priority order.
func WithAnnotators(annotators ...Annotator) Option {
	return func(g *Generator) {
		g.annotators = annotators
	}
}

// WithTitle sets the schema title.
func WithTitle(title string) Option {
	return func(g *Generator) {
		g.title = title
	}
}

// WithDescription sets the schema description.
func WithDescription(desc string) Option {
	return func(g *Generator) {
		g.description = desc
	}
}

// WithID sets the schema $id.
func WithID(id string) Option {
	return func(g *Generator) {
		g.id = id
	}
}

// WithStrict sets additionalProperties to false on objects.
func WithStrict(strict bool) Option {
	return func(g *Generator) {
		g.strict = strict
	}
}

// WithInferDefaults records observed YAML values as schema defaults
// when no annotator sets one. Scalars record their value, sequences the
// full observed list, and null or empty values a null default; objects
// record no default, since their children carry their own. Defaults are
// not recorded inside array items schemas, which describe every element
// rather than a single observed value.
func WithInferDefaults(enabled bool) Option {
	return func(g *Generator) {
		g.inferDefaults = enabled
	}
}

// Generate produces a JSON Schema from one or more YAML inputs.
// Each input is a byte slice of YAML content.
func (g *Generator) Generate(inputs ...[]byte) (*jsonschema.Schema, error) {
	var (
		result *jsonschema.Schema
		roots  []RootAnnotator
	)

	if len(inputs) == 0 {
		result = TrueSchema()
	} else {
		var schemas []*jsonschema.Schema

		for i, input := range inputs {
			schema, prepared, err := g.generateSingle(input)
			if err != nil {
				return nil, fmt.Errorf("input %d: %w", i, err)
			}

			schemas = append(schemas, schema)

			// Collect prepared root annotators in priority order:
			// input order first, annotator order within each input.
			for _, ann := range prepared {
				if ra, ok := ann.(RootAnnotator); ok {
					roots = append(roots, ra)
				}
			}
		}

		result = reduceSchemas(schemas)
	}

	// Drop the internal marker the merge uses to keep an incompatible-type
	// union fail-open across folds; it must never reach the output.
	stripTypelessUnion(result)

	// Apply root-level settings.
	result.Schema = "http://json-schema.org/draft-07/schema#"

	// Apply root schema from annotators (before CLI flag overrides).
	applyRootAnnotations(result, roots, g.strict)

	if g.title != "" {
		result.Title = g.title
	}

	if g.description != "" {
		result.Description = g.description
	}

	if g.id != "" {
		result.ID = g.id
	}

	return result, nil
}

// GenerateFiles produces a JSON Schema from one or more YAML files. It reads
// each path and delegates to [Generator.Generate]; read failures wrap
// [ErrReadInput]. Inputs merge with union semantics in path order, and merged
// metadata (defaults, descriptions, root annotations) is first-input-wins,
// so callers order the primary file first.
func (g *Generator) GenerateFiles(paths ...string) (*jsonschema.Schema, error) {
	inputs := make([][]byte, 0, len(paths))

	for _, path := range paths {
		// The *os.PathError from os.ReadFile already carries the path.
		//nolint:gosec // G304: reading caller-provided paths is the purpose of GenerateFiles.
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrReadInput, err)
		}

		inputs = append(inputs, data)
	}

	return g.Generate(inputs...)
}

// generateSingle processes a single YAML input into a schema. It returns the
// prepared annotators alongside the schema so Generate can apply root
// annotations from every input, not just the last one.
func (g *Generator) generateSingle(input []byte) (*jsonschema.Schema, []Annotator, error) {
	// Strip a UTF-8 byte-order mark; the parser would otherwise treat it
	// as part of the first property key.
	input = yamldoc.StripBOM(input)

	// Normalize line endings to LF. The YAML spec folds all line breaks to
	// LF on input, but goccy's lexer counts each CR toward its line number,
	// so under CRLF a node's reported Position.Line no longer matches its
	// physical line. Comment attribution (see [adjacentCommentRun]) reasons
	// about exact line adjacency, so on a CRLF-encoded chart it would
	// silently drop every description. Folding here keeps all
	// position-dependent logic aligned with the source.
	input = yamldoc.NormalizeLineEndings(input)

	if len(input) == 0 || yamldoc.IsBlank(input) {
		return TrueSchema(), nil, nil
	}

	input = yamldoc.DropEmptyDocuments(input)

	file, err := parser.ParseBytes(input, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidYAML, err)
	}

	// Process each document and merge schemas with union semantics.
	var (
		schemas     []*jsonschema.Schema
		allPrepared []Annotator
	)

	// Scope each document's content to the annotators that scan it (bitnami's
	// ## @param lines, norwoodj's old-style descriptions) so a multi-document
	// stream does not bleed one document's annotations into another. The split
	// is used only when it aligns 1:1 with the parsed documents; otherwise the
	// whole stream is passed, preserving prior behavior.
	var docBytes [][]byte

	if len(file.Docs) > 1 {
		docBytes = yamldoc.SplitDocumentBytes(input)
		if len(docBytes) != len(file.Docs) {
			docBytes = nil
		}
	}

	for i, doc := range file.Docs {
		// A comment-only "---" block parses to a *ast.CommentGroupNode body and
		// a leading directive (such as %YAML) to a *ast.DirectiveNode body.
		// Neither is a value, so walking it would fold a spurious empty schema
		// -- and, through the merge, a stray "null" type that defeats strict
		// mode -- into the union. Skip them like a nil body.
		switch doc.Body.(type) {
		case nil, *ast.CommentGroupNode, *ast.DirectiveNode:
			continue
		}

		// Prepare annotators per document so per-document state (e.g.
		// dadav's root-block tracking) resets at document boundaries.
		// The walk runs on a per-document copy so the receiver -- and the
		// prototype annotators it holds -- are never mutated, keeping
		// Generator safe for concurrent use.
		content := input
		if docBytes != nil {
			content = docBytes[i]
		}

		prepared := prepareAnnotators(g.annotators, content)
		allPrepared = append(allPrepared, prepared...)

		docGen := *g
		docGen.annotators = prepared
		docGen.markers = markerAnnotators(prepared)

		anchors := buildAliasResolutions(doc.Body)
		schemas = append(schemas, docGen.walkNode(doc.Body, "", anchors))
	}

	if len(schemas) == 0 {
		return TrueSchema(), allPrepared, nil
	}

	return reduceSchemas(schemas), allPrepared, nil
}

// reduceSchemas folds a non-empty slice of schemas into one with union
// semantics, merging left to right. Callers guard against an empty slice.
func reduceSchemas(schemas []*jsonschema.Schema) *jsonschema.Schema {
	result := schemas[0]
	for _, s := range schemas[1:] {
		result = mergeSchemas(result, s)
	}

	return result
}

// prepareAnnotators calls ForContent on each prototype annotator, dropping
// (with a warning) any annotator whose preparation fails.
func prepareAnnotators(annotators []Annotator, content []byte) []Annotator {
	prepared := make([]Annotator, 0, len(annotators))

	for _, ann := range annotators {
		p, err := ann.ForContent(content)
		if err != nil {
			slog.Warn("annotator prepare",
				slog.String("annotator", ann.Name()),
				slog.Any("error", err),
			)

			continue
		}

		prepared = append(prepared, p)
	}

	return prepared
}

// markerAnnotators collects the prepared annotators that implement the
// optional [MarkerAnnotator] interface, so the fallback description
// extractor can consult their marker recognizers.
func markerAnnotators(annotators []Annotator) []MarkerAnnotator {
	var markers []MarkerAnnotator

	for _, ann := range annotators {
		if m, ok := ann.(MarkerAnnotator); ok {
			markers = append(markers, m)
		}
	}

	return markers
}

// isAnnotationLine reports whether a comment line is an annotation marker of
// any recognized format: the built-in [IsAnnotationComment] list or a
// prepared annotator implementing [MarkerAnnotator]. The built-in list
// applies regardless of which annotators are enabled, so a known format's
// markers never leak into descriptions (fail open).
func (g *Generator) isAnnotationLine(s string) bool {
	if IsAnnotationComment(s) {
		return true
	}

	for _, m := range g.markers {
		if m.IsAnnotationLine(s) {
			return true
		}
	}

	return false
}

// walkNode recursively generates a schema from a YAML AST node. Depth is
// counted by the container walkers (walkMapping, inferItemsFromSequence)
// rather than here, so every recursion path -- including direct calls into
// the container walkers from merge-key and annotation handling -- consumes
// exactly one depth unit per nesting level.
func (g *Generator) walkNode(
	node ast.Node,
	keyPath string,
	anchors aliasResolutions,
) *jsonschema.Schema {
	g.visits++

	if g.visits > maxNodeVisits {
		return &jsonschema.Schema{}
	}

	node = resolveAliases(node, anchors)
	unwrapped := unwrapNode(node)

	if unwrapped == nil {
		// A broken alias or a wrapper bottoming out at nil is a null value
		// (see isNullNode), so it records a null default like a genuine null.
		schema := &jsonschema.Schema{}
		g.recordDefault(schema, node, anchors)

		return schema
	}

	var schema *jsonschema.Schema

	switch n := unwrapped.(type) {
	case *ast.MappingNode:
		schema = g.walkMapping(n, keyPath, anchors)
	case *ast.MappingValueNode:
		schema = g.walkMapping(nil, keyPath, anchors, n)
	case *ast.SequenceNode:
		schema = g.walkSequence(n, keyPath, anchors)
	default:
		// Pass the wrapped node so explicit tags reach inferType.
		schema = walkScalar(node)
	}

	g.recordDefault(schema, node, anchors)

	return schema
}

// recordDefault fills in the schema default from the observed value when
// default inference is enabled, no annotator set one, and the walk is
// outside an items schema (see the inItems field). Objects record no
// default, since their children carry their own.
func (g *Generator) recordDefault(
	schema *jsonschema.Schema,
	node ast.Node,
	anchors aliasResolutions,
) {
	if !g.inferDefaults || g.inItems != 0 || schema.Default != nil {
		return
	}

	switch unwrapNode(node).(type) {
	case *ast.MappingNode, *ast.MappingValueNode:
	case *ast.SequenceNode:
		schema.Default = g.nodeDefault(node, anchors)
	default:
		// Pass the wrapped node so explicit tags coerce the value.
		schema.Default = scalarDefault(node)
	}
}

// walkMapping processes a mapping node into an object schema.
func (g *Generator) walkMapping(
	mn *ast.MappingNode,
	keyPath string,
	anchors aliasResolutions,
	extraValues ...*ast.MappingValueNode,
) *jsonschema.Schema {
	g.depth++
	defer func() { g.depth-- }()

	g.visits++

	if g.depth > maxWalkDepth || g.visits > maxNodeVisits {
		return &jsonschema.Schema{}
	}

	schema := &jsonschema.Schema{
		Type:       typeObject,
		Properties: make(map[string]*jsonschema.Schema),
	}

	if g.strict {
		schema.AdditionalProperties = FalseSchema()
	} else {
		schema.AdditionalProperties = TrueSchema()
	}

	// Callers pass either a mapping node or extra values, never both, so
	// no append is needed (appending to mn.Values could mutate the AST's
	// backing array).
	values := extraValues

	if mn != nil {
		values = mn.Values
	}

	var (
		propertyOrder []string
		orderSeen     = make(map[string]bool)
	)

	addToOrder := func(key string) {
		if !orderSeen[key] {
			propertyOrder = append(propertyOrder, key)
			orderSeen[key] = true
		}
	}

	// Explicit property annotations settle whether a key is omitted or
	// required; a "<<" merge key must honor those decisions no matter which
	// side the parser reached first.
	decisions := &explicitDecisions{
		skipped:  make(map[string]bool),
		optedOut: make(map[string]bool),
	}

	for _, mvn := range values {
		if _, ok := mvn.Key.(*ast.MergeKeyNode); ok {
			g.handleMergeKey(mvn, keyPath, anchors, schema, addToOrder, decisions)

			continue
		}

		g.handleProperty(mvn, keyPath, anchors, schema, addToOrder, decisions)
	}

	// A skipped key may have been inserted (and ordered) by a merge key before
	// the explicit annotation removed it; drop any order entry whose property
	// no longer exists so order and properties stay in sync.
	propertyOrder = slices.DeleteFunc(propertyOrder, func(k string) bool {
		_, ok := schema.Properties[k]

		return !ok
	})

	schema.PropertyOrder = propertyOrder

	if len(schema.Properties) == 0 {
		schema.Properties = nil
		schema.PropertyOrder = nil
	}

	return schema
}

// explicitDecisions records the keys an explicit property annotation has
// settled within one mapping, so a "<<" merge key processed in a different
// source position cannot override them: a skipped key stays omitted and a
// de-required key stays optional, regardless of which side the parser saw
// first.
type explicitDecisions struct {
	skipped  map[string]bool // keys an explicit annotation omitted (hidden/@skip)
	optedOut map[string]bool // keys an explicit required:false de-required
}

// handleMergeKey processes a YAML merge key (<<) and adds its properties.
func (g *Generator) handleMergeKey(
	mvn *ast.MappingValueNode,
	keyPath string,
	anchors aliasResolutions,
	schema *jsonschema.Schema,
	addToOrder func(string),
	decisions *explicitDecisions,
) {
	mergeValue := resolveAliases(mvn.Value, anchors)
	mergeValue = unwrapNode(mergeValue)

	switch mv := mergeValue.(type) {
	case *ast.MappingNode:
		g.mergeMappingInto(schema, g.walkMapping(mv, keyPath, anchors), addToOrder, decisions)

	case *ast.SequenceNode:
		for _, seqVal := range mv.Values {
			resolved := resolveAliases(seqVal, anchors)
			resolved = unwrapNode(resolved)

			mappingNode, ok := resolved.(*ast.MappingNode)
			if !ok {
				continue
			}

			g.mergeMappingInto(schema, g.walkMapping(mappingNode, keyPath, anchors), addToOrder, decisions)
		}
	}
}

// mergeMappingInto adds a merge-key mapping's properties and required keys
// into schema. Existing properties win (explicit keys override '<<' merges
// regardless of position), a key an explicit annotation skipped is never
// re-inserted, required keys are deduplicated, and a key an explicit
// required:false de-required (or skipped) is not marked required.
func (g *Generator) mergeMappingInto(
	schema, mergeSchema *jsonschema.Schema,
	addToOrder func(string),
	decisions *explicitDecisions,
) {
	for _, k := range propertyKeys(mergeSchema) {
		if _, exists := schema.Properties[k]; exists || decisions.skipped[k] {
			continue
		}

		schema.Properties[k] = mergeSchema.Properties[k]
		addToOrder(k)
	}

	for _, k := range mergeSchema.Required {
		if decisions.optedOut[k] || decisions.skipped[k] {
			continue
		}

		addRequired(schema, k)
	}
}

// addRequired appends key to schema.Required unless already present.
func addRequired(schema *jsonschema.Schema, key string) {
	if !slices.Contains(schema.Required, key) {
		schema.Required = append(schema.Required, key)
	}
}

// removeRequired deletes key from schema.Required if present, so an explicit
// required:false can clear a key a merge mapping marked required.
func removeRequired(schema *jsonschema.Schema, key string) {
	schema.Required = slices.DeleteFunc(schema.Required, func(k string) bool {
		return k == key
	})
}

// handleProperty processes a single key-value pair in a mapping.
func (g *Generator) handleProperty(
	mvn *ast.MappingValueNode,
	keyPath string,
	anchors aliasResolutions,
	schema *jsonschema.Schema,
	addToOrder func(string),
	decisions *explicitDecisions,
) {
	keyName := keyText(mvn.Key, anchors)

	childPath := keyName
	if keyPath != "" {
		childPath = keyPath + "." + keyName
	}

	annotation := g.annotate(mvn, childPath)
	if annotation != nil && annotation.Skip {
		// Record the skip and undo any property a preceding "<<" merge key
		// already inserted, so the subtree is omitted regardless of source
		// order. A merge key after this point also honors decisions.skipped.
		decisions.skipped[keyName] = true

		delete(schema.Properties, keyName)
		removeRequired(schema, keyName)

		return
	}

	// Keep tag and anchor wrappers on the value node so explicit tags
	// reach inferType; buildChildSchema unwraps for structural checks.
	valueNode := resolveAliases(mvn.Value, anchors)

	childSchema := g.buildChildSchema(mvn, childPath, anchors, valueNode, annotation)

	// Description precedence lives entirely in buildChildSchema: in the
	// annotated path childSchema is annotation.Schema itself, so its
	// description is already the annotator's, with the comment fallback
	// filling only an empty one.

	schema.Properties[keyName] = childSchema
	addToOrder(keyName)

	switch {
	case annotation == nil || annotation.HasRequired == nil:
		// No explicit signal: leave any merge-key-inherited required as is.
	case *annotation.HasRequired:
		addRequired(schema, keyName)
	default:
		// An explicit required:false overrides a required key a "<<" merge
		// brought in from the merged mapping: the property must not stay
		// required against the annotation's intent. Recording the opt-out
		// keeps a merge key processed after this point from re-adding it.
		decisions.optedOut[keyName] = true

		removeRequired(schema, keyName)
	}
}

// keyText returns the plain text of a mapping key: quoted string keys unwrap
// to their value so property names and key paths carry no quote characters,
// anchor and tag wrappers resolve to the underlying key, and alias keys
// resolve through the anchor map -- so "&k foo", "!!str foo", and "*k" all
// yield the key text a YAML loader would use rather than raw source syntax,
// which under [WithStrict] would emit a property name the source document
// itself fails to match. A broken alias key resolves to null, which YAML
// loaders render as the string "null".
func keyText(key ast.MapKeyNode, anchors aliasResolutions) string {
	node := unwrapNode(resolveAliases(key, anchors))
	if node == nil {
		return typeNull
	}

	if s, ok := node.(*ast.StringNode); ok {
		return s.Value
	}

	return node.String()
}

// buildChildSchema creates a schema for a child property, combining annotations
// and structural inference.
func (g *Generator) buildChildSchema(
	mvn *ast.MappingValueNode,
	childPath string,
	anchors aliasResolutions,
	valueNode ast.Node,
	annotation *AnnotationResult,
) *jsonschema.Schema {
	if annotation == nil || annotation.Schema == nil {
		childSchema := g.walkNode(valueNode, childPath, anchors)
		if childSchema.Description == "" {
			childSchema.Description = extractComment(mvn, g.isAnnotationLine)
		}

		return childSchema
	}

	childSchema := annotation.Schema

	inferred := inferType(valueNode)

	// If annotation doesn't specify type, infer it. The fill is skipped when
	// the annotation's enum or const contradicts the inferred type: grafting
	// the structural type onto an incompatible value set would leave a schema
	// no value satisfies (fail closed), mirroring the value-set guard
	// mergeSchemaFields applies to the annotator-vs-annotator fill.
	if childSchema.Type == "" && len(childSchema.Types) == 0 && inferred != "" &&
		valueSetFitsType(enumValues(childSchema), &jsonschema.Schema{Type: inferred}) {
		childSchema.Type = inferred
	}

	// A null-only annotated type (e.g. bitnami's [nullable] without a type, or
	// an inline type:null) widens with the value's inferred type so the schema
	// does not reject the concrete value present in the source. A concrete
	// annotated type is authoritative and stands even over a null value. The
	// widening needs no value-set guard: it only adds a type to the union, so
	// it never rejects a value the null-only annotation accepted.
	if isNullOnlyType(childSchema) && inferred != "" && inferred != typeNull {
		SetSchemaType(childSchema, []string{inferred, typeNull})
	}

	// Structural recursion looks through tag and anchor wrappers.
	structuralNode := unwrapNode(valueNode)

	// Distinguish an annotator-set additionalProperties (authoritative) from
	// one fillObjectFromStructure inherits off the structural walk below,
	// which only fills in when the annotation leaves the field nil.
	annotatedAP := childSchema.AdditionalProperties != nil

	// For object types, recurse into children.
	if hasType(childSchema, typeObject) && childSchema.Properties == nil {
		g.fillObjectFromStructure(childSchema, structuralNode, childPath, anchors, annotation)
	}

	// Skip- and merge-properties annotations declare the mapping's keys
	// dynamic, so the structural walk's additionalProperties -- false under
	// [WithStrict] -- must not survive on a node whose property map those
	// annotations hide: with the properties stripped it would validate only
	// the empty object and reject the source file (fail open). Only the
	// structurally inherited value resets; an annotator-set
	// additionalProperties stands, and the mergeProperties fold below
	// installs its own merged schema when there are properties to fold.
	if (annotation.SkipProperties || annotation.MergeProperties) &&
		!annotatedAP && childSchema.AdditionalProperties != nil {
		childSchema.AdditionalProperties = TrueSchema()
	}

	// For array types, recurse into items. Skip when an annotator already set a
	// tuple items array (ItemsArray): a single inferred Items schema beside it
	// sets both forms of the keyword, which the marshaler rejects (fail closed).
	if hasType(childSchema, typeArray) && childSchema.Items == nil && childSchema.ItemsArray == nil {
		if seqNode, ok := structuralNode.(*ast.SequenceNode); ok {
			childSchema.Items = g.inferItemsFromSequence(seqNode, childPath, anchors)
		}
	}

	// Apply mergeProperties before skipProperties. Both can be set at once --
	// mergeAnnotations ORs them, so two annotators can each contribute one, or
	// one annotation line can set both -- and stripping first would leave
	// mergeProperties with no Properties to fold, silently dropping the child
	// schemas. Folding first preserves them in additionalProperties; the strip
	// then clears the now-empty Properties map.
	if annotation.MergeProperties && childSchema.Properties != nil {
		childSchema.AdditionalProperties = mergePropertySchemas(childSchema)
		childSchema.Properties = nil
		childSchema.PropertyOrder = nil
	}

	// Apply skipProperties: strip Properties from output.
	if annotation.SkipProperties {
		childSchema.Properties = nil
		childSchema.PropertyOrder = nil
	}

	// The observed value fills in the default when no annotator set one,
	// regardless of the annotated type (descriptive, fail-open).
	g.recordDefault(childSchema, valueNode, anchors)

	// A plain comment fills in the description when no annotator set one:
	// extract as much as possible (best-effort) rather than letting an
	// annotation without a description suppress the comment fallback.
	if childSchema.Description == "" {
		childSchema.Description = extractComment(mvn, g.isAnnotationLine)
	}

	return childSchema
}

// fillObjectFromStructure copies the structural recursion's object schema
// (properties, order, required, additionalProperties) onto an annotated
// object schema whose annotation left those fields unset.
func (g *Generator) fillObjectFromStructure(
	childSchema *jsonschema.Schema,
	structuralNode ast.Node,
	childPath string,
	anchors aliasResolutions,
	annotation *AnnotationResult,
) {
	mappingNode, ok := structuralNode.(*ast.MappingNode)
	if !ok {
		return
	}

	structural := g.walkMapping(mappingNode, childPath, anchors)
	childSchema.Properties = structural.Properties
	childSchema.PropertyOrder = structural.PropertyOrder

	// Child annotations mark their keys required on the structural schema;
	// carry them onto the annotated schema. Skipped when the property map
	// is stripped afterwards, since the required keys would reference
	// properties the annotation deliberately leaves open.
	if !annotation.SkipProperties && !annotation.MergeProperties {
		for _, k := range structural.Required {
			addRequired(childSchema, k)
		}
	}

	if childSchema.AdditionalProperties == nil {
		childSchema.AdditionalProperties = structural.AdditionalProperties
	}
}

// walkSequence processes a sequence node into an array schema.
func (g *Generator) walkSequence(
	seq *ast.SequenceNode,
	keyPath string,
	anchors aliasResolutions,
) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:  typeArray,
		Items: g.inferItemsFromSequence(seq, keyPath, anchors),
	}
}

// inferItemsFromSequence infers the items schema from a sequence node's
// values. Returning nil leaves the items constraint unset, which both the
// empty-sequence case and the depth cutoff use to fail open.
func (g *Generator) inferItemsFromSequence(
	seq *ast.SequenceNode,
	keyPath string,
	anchors aliasResolutions,
) *jsonschema.Schema {
	g.depth++
	defer func() { g.depth-- }()

	g.visits++

	if g.depth > maxWalkDepth || g.visits > maxNodeVisits {
		return nil
	}

	// Suppress per-element defaults inside the items schema (see the
	// inItems field); the sequence node itself already carries the full
	// list as its default.
	g.inItems++
	defer func() { g.inItems-- }()

	if len(seq.Values) == 0 {
		return nil
	}

	// Resolve each element's aliases once, keeping tag/anchor wrappers so
	// explicit tags still reach inferType. The resolved nodes drive both the
	// all-mappings check and the walk; resolving here (not just in the
	// all-mappings branch) means alias-valued scalar elements keep their type
	// in the structural fallback below.
	resolved := make([]ast.Node, 0, len(seq.Values))
	anyMapping, onlyMappingsAndNulls := false, true

	for _, val := range seq.Values {
		node := resolveAliases(val, anchors)
		resolved = append(resolved, node)

		_, isMapping := unwrapNode(node).(*ast.MappingNode)

		// A null element does not break the all-mappings path: it widens the
		// merged item schema to allow null while keeping the property schemas.
		switch {
		case isMapping:
			anyMapping = true
		case isNullNode(node):
		default:
			onlyMappingsAndNulls = false
		}
	}

	// When every element is a mapping (nulls allowed among them), merge their
	// object schemas so the per-property item schemas survive; each null walks
	// to the empty schema and widens the result to [object, null]. Falling
	// back to inferItemsSchema here would drop every property schema.
	if anyMapping && onlyMappingsAndNulls {
		var result *jsonschema.Schema

		for _, node := range resolved {
			result = mergeSchemas(result, g.walkNode(node, keyPath, anchors))
		}

		return result
	}

	// For scalar arrays, just use type inference.
	return inferItemsSchema(resolved)
}

// walkScalar generates a schema for a scalar value node.
func walkScalar(node ast.Node) *jsonschema.Schema {
	t := inferType(node)
	if t == "" {
		return &jsonschema.Schema{}
	}

	return &jsonschema.Schema{Type: t}
}

// scalarDefault converts a scalar node's observed value to a JSON default.
// The node arrives alias-resolved but still wrapped, so explicit tags
// coerce the value the same way a YAML loader would. Values with no JSON
// representation (NaN, infinities) yield no default.
func scalarDefault(node ast.Node) json.RawMessage {
	v, ok := scalarGoValue(node)
	if !ok {
		return nil
	}

	return DefaultValue(v)
}

// scalarGoValue converts a scalar node to its plain Go value. Block
// scalars read the inner string node's text directly; both the literal
// node and its re-serialized forms carry the block header.
func scalarGoValue(node ast.Node) (any, bool) {
	// Unwrap before the null check so a known-tag-on-empty-scalar
	// (e.g. "v: !!int" with no value) is treated as the null it actually
	// holds, matching inferType which suppresses the type for the same node.
	// Reading the tag-wrapped node instead lets resolveTagged report the
	// tagged type and coerce the absent value to a concrete zero (0, "").
	if isNullNode(unwrapNode(node)) {
		return nil, true
	}

	if lit, ok := unwrapNode(node).(*ast.LiteralNode); ok {
		return lit.Value.Value, true
	}

	var v any

	err := yaml.NodeToValue(node, &v)
	if err != nil {
		return nil, false
	}

	return v, true
}

// nodeDefault converts a container node's observed value to a JSON
// default, resolving aliases along the way. Conversion is all-or-nothing:
// any unconvertible part (alias cycles, merge keys, exceeded walk
// budgets) yields no default rather than a partial value.
func (g *Generator) nodeDefault(node ast.Node, anchors aliasResolutions) json.RawMessage {
	v, ok := g.astToGoValue(node, anchors)
	if !ok {
		return nil
	}

	return DefaultValue(v)
}

// astToGoValue converts an AST subtree to a plain Go value, resolving
// aliases via the anchor map. Sequences may alias anchors defined outside
// the subtree, which [yaml.NodeToValue] cannot resolve, so containers
// convert manually. They consume the walk depth budget and a separate
// default-visit budget -- the only guard against alias cycles inside
// sequences -- so pathological inputs fail open to no default instead of
// hanging. The default-visit budget is kept distinct from the structural
// walk's so recording defaults never shrinks structural coverage.
func (g *Generator) astToGoValue(node ast.Node, anchors aliasResolutions) (any, bool) {
	g.defaultVisits++

	if g.defaultVisits > maxNodeVisits {
		return nil, false
	}

	node = resolveAliases(node, anchors)

	switch n := unwrapNode(node).(type) {
	case nil:
		return nil, false
	case *ast.SequenceNode:
		return g.sequenceToGoValue(n, anchors)
	case *ast.MappingNode:
		return g.mappingToGoValue(n.Values, anchors)
	case *ast.MappingValueNode:
		return g.mappingToGoValue([]*ast.MappingValueNode{n}, anchors)
	default:
		// Pass the wrapped node so explicit tags coerce the value.
		return scalarGoValue(node)
	}
}

// sequenceToGoValue converts a sequence node to a []any, initialized
// non-nil so an empty sequence marshals as [] rather than null.
func (g *Generator) sequenceToGoValue(
	seq *ast.SequenceNode,
	anchors aliasResolutions,
) (any, bool) {
	g.depth++
	defer func() { g.depth-- }()

	if g.depth > maxWalkDepth {
		return nil, false
	}

	out := make([]any, 0, len(seq.Values))

	for _, val := range seq.Values {
		v, ok := g.astToGoValue(val, anchors)
		if !ok {
			return nil, false
		}

		out = append(out, v)
	}

	return out, true
}

// mappingToGoValue converts mapping entries to a map[string]any. Merge
// keys (<<) fail open to no value: they are rare inside sequences, and
// expanding them here would duplicate the schema walk's merge handling.
func (g *Generator) mappingToGoValue(
	values []*ast.MappingValueNode,
	anchors aliasResolutions,
) (any, bool) {
	g.depth++
	defer func() { g.depth-- }()

	if g.depth > maxWalkDepth {
		return nil, false
	}

	out := make(map[string]any, len(values))

	for _, mvn := range values {
		if _, ok := mvn.Key.(*ast.MergeKeyNode); ok {
			return nil, false
		}

		v, ok := g.astToGoValue(mvn.Value, anchors)
		if !ok {
			return nil, false
		}

		out[keyText(mvn.Key, anchors)] = v
	}

	return out, true
}

// annotate runs all enabled annotators on a node and returns the merged result.
func (g *Generator) annotate(node ast.Node, keyPath string) *AnnotationResult {
	if len(g.annotators) == 0 {
		return nil
	}

	results := make([]*AnnotationResult, 0, len(g.annotators))

	for _, ann := range g.annotators {
		result := ann.Annotate(node, keyPath)
		results = append(results, result)
	}

	return mergeAnnotations(results)
}

// applyRootAnnotations merges root-level schema properties from prepared
// root annotators, in priority order (first annotator wins per field). Only
// a specific subset of fields are propagated: title, description, $ref,
// examples, deprecated, readOnly, writeOnly, additionalProperties, and x-*
// custom annotations. AdditionalProperties overrides the structural default
// already set during the walk, so the first annotator that sets it wins
// rather than the first non-nil check -- except in strict mode, where the
// generator-level setting outranks annotators (per the documented rule that
// CLI-level values override annotator values) and the root keeps the
// FalseSchema set during the walk.
func applyRootAnnotations(schema *jsonschema.Schema, roots []RootAnnotator, strict bool) {
	apSet := strict

	for _, ra := range roots {
		root := ra.RootSchema()
		if root == nil {
			continue
		}

		// First non-empty wins across annotators, the same first-wins rule the
		// merge expresses with cmp.Or and sticky OR; Examples (a slice) and
		// AdditionalProperties (gated by apSet) cannot collapse cleanly.
		schema.Title = cmp.Or(schema.Title, root.Title)
		schema.Description = cmp.Or(schema.Description, root.Description)
		schema.Ref = cmp.Or(schema.Ref, root.Ref)

		if schema.Examples == nil && root.Examples != nil {
			schema.Examples = root.Examples
		}

		schema.Deprecated = schema.Deprecated || root.Deprecated
		schema.ReadOnly = schema.ReadOnly || root.ReadOnly
		schema.WriteOnly = schema.WriteOnly || root.WriteOnly

		if !apSet && root.AdditionalProperties != nil {
			schema.AdditionalProperties = root.AdditionalProperties
			apSet = true
		}

		schema.Extra = mergeExtraInto(schema.Extra, root.Extra)
	}

	// A root $ref makes the document a reference. Draft 7 ignores the siblings
	// of $ref, so the structural type and properties the walk produced would be
	// silently inert beside it -- and actively misleading if the referent
	// constrains differently. Drop them so the output is the reference the
	// annotation asked for. Only a root annotator can set Ref here, since the
	// structural walk never does.
	if schema.Ref != "" {
		schema.Type = ""
		schema.Types = nil
		schema.Properties = nil
		schema.PropertyOrder = nil
		schema.AdditionalProperties = nil
		schema.Required = nil
	}
}

// isNullOnlyType reports whether a schema's type constraint permits only
// null values, normalizing the scalar Type and the Types union through the
// same splitter the merge logic uses.
func isNullOnlyType(s *jsonschema.Schema) bool {
	core, nullable := splitNullType(typeList(s))

	return nullable && len(core) == 0
}

// mergePropertySchemas combines a schema's property schemas into a single
// schema for use as additionalProperties. Properties merge in propertyKeys
// order so the result is deterministic.
func mergePropertySchemas(s *jsonschema.Schema) *jsonschema.Schema {
	var merged *jsonschema.Schema

	for _, k := range propertyKeys(s) {
		if merged == nil {
			merged = s.Properties[k]

			continue
		}

		merged = mergeSchemas(merged, s.Properties[k])
	}

	if merged == nil {
		return TrueSchema()
	}

	return merged
}

// aliasResolutions maps each alias node to the anchor value it resolves to.
// YAML binds an alias to the nearest anchor of the same name defined before
// it, so a single document-global map keyed by name would resolve every alias
// to the last definition and leak a redefined anchor's later value back to
// earlier alias sites. Keying by alias node instead records each alias's own
// nearest preceding definition; a nil entry means no anchor was in scope and
// the alias resolves to null.
type aliasResolutions map[*ast.AliasNode]ast.Node

// buildAliasResolutions walks the document in source order, tracking the
// anchor definition in scope for each name, and records what every alias
// resolves to at its own position. [ast.Walk] is pre-order and visits
// children in source order, so an anchor is recorded before any alias that
// follows it and a redefinition only affects aliases after it.
func buildAliasResolutions(node ast.Node) aliasResolutions {
	v := &aliasResolver{
		current:  make(map[string]ast.Node),
		resolved: make(aliasResolutions),
	}

	ast.Walk(v, node)

	return v.resolved
}

type aliasResolver struct {
	current  map[string]ast.Node
	resolved aliasResolutions
}

// Visit implements the [ast.Visitor] interface, threading the running anchor
// scope through the document-order walk.
func (v *aliasResolver) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.AnchorNode:
		v.current[n.Name.String()] = n.Value
	case *ast.AliasNode:
		v.resolved[n] = v.current[n.Value.String()]
	}

	return v
}

// resolveAliases returns the value an alias node resolves to, or the node
// unchanged when it is not an alias. An alias with no in-scope anchor (an
// unrecorded or nil resolution) is treated as null.
func resolveAliases(node ast.Node, resolved aliasResolutions) ast.Node {
	if node == nil {
		return nil
	}

	alias, ok := node.(*ast.AliasNode)
	if !ok {
		return node
	}

	return resolved[alias]
}
