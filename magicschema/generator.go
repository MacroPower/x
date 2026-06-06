package magicschema

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/google/jsonschema-go/jsonschema"
)

// Sentinel errors returned by the generator.
var (
	ErrInvalidYAML   = errors.New("invalid yaml")
	ErrInvalidOption = errors.New("invalid option")
	ErrReadInput     = errors.New("read input")
	ErrWriteOutput   = errors.New("write output")
)

// Generator produces JSON Schema from YAML input.
type Generator struct {
	title       string
	description string
	id          string
	annotators  []Annotator
	strict      bool

	// Walk recursion depth on the per-document copy of the Generator,
	// bounding alias cycles and nesting deeper than maxWalkDepth.
	depth int
}

// maxWalkDepth bounds schema-walk recursion. YAML alias cycles (an anchor
// whose subtree aliases back to itself) would otherwise recurse forever;
// subtrees past the bound fail open to the empty schema.
const maxWalkDepth = 1000

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

		result = schemas[0]

		for i := 1; i < len(schemas); i++ {
			result = mergeSchemas(result, schemas[i])
		}
	}

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

// generateSingle processes a single YAML input into a schema. It returns the
// prepared annotators alongside the schema so Generate can apply root
// annotations from every input, not just the last one.
func (g *Generator) generateSingle(input []byte) (*jsonschema.Schema, []Annotator, error) {
	if len(input) == 0 || isBlank(input) {
		return TrueSchema(), nil, nil
	}

	file, err := parser.ParseBytes(input, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidYAML, err)
	}

	// Process each document and merge schemas with union semantics.
	var (
		schemas     []*jsonschema.Schema
		allPrepared []Annotator
	)

	for _, doc := range file.Docs {
		if doc.Body == nil {
			continue
		}

		// Prepare annotators per document so per-document state (e.g.
		// dadav's root-block tracking) resets at document boundaries.
		// The walk runs on a per-document copy so the receiver -- and the
		// prototype annotators it holds -- are never mutated, keeping
		// Generator safe for concurrent use.
		prepared := prepareAnnotators(g.annotators, input)
		allPrepared = append(allPrepared, prepared...)

		docGen := *g
		docGen.annotators = prepared

		anchors := buildAnchorMap(doc.Body)
		schemas = append(schemas, docGen.walkNode(doc.Body, "", anchors))
	}

	if len(schemas) == 0 {
		return TrueSchema(), allPrepared, nil
	}

	result := schemas[0]
	for i := 1; i < len(schemas); i++ {
		result = mergeSchemas(result, schemas[i])
	}

	return result, allPrepared, nil
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

// walkNode recursively generates a schema from a YAML AST node.
func (g *Generator) walkNode(
	node ast.Node,
	keyPath string,
	anchors map[string]ast.Node,
) *jsonschema.Schema {
	g.depth++
	defer func() { g.depth-- }()

	if g.depth > maxWalkDepth {
		return &jsonschema.Schema{}
	}

	node = resolveAliases(node, anchors)
	node = unwrapNode(node)

	if node == nil {
		return &jsonschema.Schema{}
	}

	switch n := node.(type) {
	case *ast.MappingNode:
		return g.walkMapping(n, keyPath, anchors)
	case *ast.MappingValueNode:
		return g.walkMapping(nil, keyPath, anchors, n)
	case *ast.SequenceNode:
		return g.walkSequence(n, keyPath, anchors)
	default:
		return g.walkScalar(node)
	}
}

// walkMapping processes a mapping node into an object schema.
func (g *Generator) walkMapping(
	mn *ast.MappingNode,
	keyPath string,
	anchors map[string]ast.Node,
	extraValues ...*ast.MappingValueNode,
) *jsonschema.Schema {
	g.depth++
	defer func() { g.depth-- }()

	if g.depth > maxWalkDepth {
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

	for _, mvn := range values {
		if _, ok := mvn.Key.(*ast.MergeKeyNode); ok {
			g.handleMergeKey(mvn, keyPath, anchors, schema, addToOrder)

			continue
		}

		g.handleProperty(mvn, keyPath, anchors, schema, addToOrder)
	}

	schema.PropertyOrder = propertyOrder

	if len(schema.Properties) == 0 {
		schema.Properties = nil
		schema.PropertyOrder = nil
	}

	return schema
}

// handleMergeKey processes a YAML merge key (<<) and adds its properties.
func (g *Generator) handleMergeKey(
	mvn *ast.MappingValueNode,
	keyPath string,
	anchors map[string]ast.Node,
	schema *jsonschema.Schema,
	addToOrder func(string),
) {
	mergeValue := resolveAliases(mvn.Value, anchors)
	mergeValue = unwrapNode(mergeValue)

	switch mv := mergeValue.(type) {
	case *ast.MappingNode:
		g.mergeMappingInto(schema, g.walkMapping(mv, keyPath, anchors), addToOrder)

	case *ast.SequenceNode:
		for _, seqVal := range mv.Values {
			resolved := resolveAliases(seqVal, anchors)
			resolved = unwrapNode(resolved)

			mappingNode, ok := resolved.(*ast.MappingNode)
			if !ok {
				continue
			}

			g.mergeMappingInto(schema, g.walkMapping(mappingNode, keyPath, anchors), addToOrder)
		}
	}
}

// mergeMappingInto adds a merge-key mapping's properties and required keys
// into schema. Existing properties win (explicit keys override '<<' merges
// regardless of position) and required keys are deduplicated.
func (g *Generator) mergeMappingInto(
	schema, mergeSchema *jsonschema.Schema,
	addToOrder func(string),
) {
	for _, k := range propertyKeys(mergeSchema) {
		if _, exists := schema.Properties[k]; !exists {
			schema.Properties[k] = mergeSchema.Properties[k]
			addToOrder(k)
		}
	}

	for _, k := range mergeSchema.Required {
		addRequired(schema, k)
	}
}

// addRequired appends key to schema.Required unless already present.
func addRequired(schema *jsonschema.Schema, key string) {
	if !slices.Contains(schema.Required, key) {
		schema.Required = append(schema.Required, key)
	}
}

// handleProperty processes a single key-value pair in a mapping.
func (g *Generator) handleProperty(
	mvn *ast.MappingValueNode,
	keyPath string,
	anchors map[string]ast.Node,
	schema *jsonschema.Schema,
	addToOrder func(string),
) {
	keyName := keyText(mvn.Key)

	childPath := keyName
	if keyPath != "" {
		childPath = keyPath + "." + keyName
	}

	annotation := g.annotate(mvn, childPath)
	if annotation != nil && annotation.Skip {
		return
	}

	valueNode := resolveAliases(mvn.Value, anchors)
	valueNode = unwrapNode(valueNode)

	childSchema := g.buildChildSchema(mvn, childPath, anchors, valueNode, annotation)

	// If annotation provided a description, prefer it.
	if annotation != nil && annotation.Schema != nil && annotation.Schema.Description != "" {
		childSchema.Description = annotation.Schema.Description
	}

	schema.Properties[keyName] = childSchema
	addToOrder(keyName)

	if annotation != nil && annotation.HasRequired != nil && *annotation.HasRequired {
		addRequired(schema, keyName)
	}
}

// keyText returns the plain text of a mapping key, unwrapping quoted string
// keys so property names and key paths carry no quote characters.
func keyText(key ast.MapKeyNode) string {
	if s, ok := key.(*ast.StringNode); ok {
		return s.Value
	}

	return key.String()
}

// buildChildSchema creates a schema for a child property, combining annotations
// and structural inference.
func (g *Generator) buildChildSchema(
	mvn *ast.MappingValueNode,
	childPath string,
	anchors map[string]ast.Node,
	valueNode ast.Node,
	annotation *AnnotationResult,
) *jsonschema.Schema {
	if annotation == nil || annotation.Schema == nil {
		childSchema := g.walkNode(valueNode, childPath, anchors)
		if childSchema.Description == "" {
			childSchema.Description = extractComment(mvn)
		}

		return childSchema
	}

	childSchema := annotation.Schema

	// If annotation doesn't specify type, infer it.
	if childSchema.Type == "" && len(childSchema.Types) == 0 {
		if inferred := inferType(valueNode); inferred != "" {
			childSchema.Type = inferred
		}
	}

	// A null-only annotated type (e.g. bitnami's [nullable] without a type,
	// or an inline type:null) widens with the value's inferred type so the
	// schema does not reject the concrete value present in the source.
	if isNullOnlyType(childSchema) {
		if inferred := inferType(valueNode); inferred != "" && inferred != typeNull {
			childSchema.Type = ""
			childSchema.Types = []string{inferred, typeNull}
		}
	}

	// For object types, recurse into children.
	if (childSchema.Type == typeObject || isObjectType(childSchema)) && childSchema.Properties == nil {
		if mappingNode, ok := valueNode.(*ast.MappingNode); ok {
			structural := g.walkMapping(mappingNode, childPath, anchors)
			childSchema.Properties = structural.Properties
			childSchema.PropertyOrder = structural.PropertyOrder

			if childSchema.AdditionalProperties == nil {
				childSchema.AdditionalProperties = structural.AdditionalProperties
			}
		}
	}

	// For array types, recurse into items.
	if (childSchema.Type == typeArray || isArrayType(childSchema)) && childSchema.Items == nil {
		if seqNode, ok := valueNode.(*ast.SequenceNode); ok {
			childSchema.Items = g.inferItemsFromSequence(seqNode, childPath, anchors)
		}
	}

	// Apply skipProperties: strip Properties from output.
	if annotation.SkipProperties {
		childSchema.Properties = nil
		childSchema.PropertyOrder = nil
	}

	// Apply mergeProperties: merge child properties into additionalProperties.
	if annotation.MergeProperties && childSchema.Properties != nil {
		childSchema.AdditionalProperties = mergePropertySchemas(childSchema)
		childSchema.Properties = nil
		childSchema.PropertyOrder = nil
	}

	return childSchema
}

// walkSequence processes a sequence node into an array schema.
func (g *Generator) walkSequence(
	seq *ast.SequenceNode,
	keyPath string,
	anchors map[string]ast.Node,
) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:  typeArray,
		Items: g.inferItemsFromSequence(seq, keyPath, anchors),
	}
}

// inferItemsFromSequence infers the items schema from a sequence node's values.
func (g *Generator) inferItemsFromSequence(
	seq *ast.SequenceNode,
	keyPath string,
	anchors map[string]ast.Node,
) *jsonschema.Schema {
	if len(seq.Values) == 0 {
		return nil
	}

	// Check if all elements are mappings -- if so, merge their schemas.
	allMappings := true

	for _, val := range seq.Values {
		resolved := resolveAliases(val, anchors)
		resolved = unwrapNode(resolved)

		if _, ok := resolved.(*ast.MappingNode); !ok {
			allMappings = false

			break
		}
	}

	if allMappings {
		var itemSchemas []*jsonschema.Schema

		for _, val := range seq.Values {
			resolved := resolveAliases(val, anchors)
			resolved = unwrapNode(resolved)

			s := g.walkNode(resolved, keyPath, anchors)
			itemSchemas = append(itemSchemas, s)
		}

		result := itemSchemas[0]

		for i := 1; i < len(itemSchemas); i++ {
			result = mergeSchemas(result, itemSchemas[i])
		}

		return result
	}

	// For scalar arrays, just use type inference.
	return inferItemsSchema(seq)
}

// walkScalar generates a schema for a scalar value node.
func (g *Generator) walkScalar(node ast.Node) *jsonschema.Schema {
	t := inferType(node)
	if t == "" {
		return &jsonschema.Schema{}
	}

	return &jsonschema.Schema{Type: t}
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

		if schema.Title == "" && root.Title != "" {
			schema.Title = root.Title
		}

		if schema.Description == "" && root.Description != "" {
			schema.Description = root.Description
		}

		if schema.Ref == "" && root.Ref != "" {
			schema.Ref = root.Ref
		}

		if schema.Examples == nil && root.Examples != nil {
			schema.Examples = root.Examples
		}

		if !schema.Deprecated && root.Deprecated {
			schema.Deprecated = root.Deprecated
		}

		if !schema.ReadOnly && root.ReadOnly {
			schema.ReadOnly = root.ReadOnly
		}

		if !schema.WriteOnly && root.WriteOnly {
			schema.WriteOnly = root.WriteOnly
		}

		if !apSet && root.AdditionalProperties != nil {
			schema.AdditionalProperties = root.AdditionalProperties
			apSet = true
		}

		if root.Extra != nil {
			if schema.Extra == nil {
				schema.Extra = make(map[string]any)
			}

			for k, v := range root.Extra {
				if _, exists := schema.Extra[k]; !exists {
					schema.Extra[k] = v
				}
			}
		}
	}
}

// isNullOnlyType reports whether a schema's type constraint permits only
// null values.
func isNullOnlyType(s *jsonschema.Schema) bool {
	if s.Type == typeNull && len(s.Types) == 0 {
		return true
	}

	if s.Type != "" || len(s.Types) == 0 {
		return false
	}

	for _, t := range s.Types {
		if t != typeNull {
			return false
		}
	}

	return true
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

// buildAnchorMap walks the AST and collects all anchor definitions.
func buildAnchorMap(node ast.Node) map[string]ast.Node {
	anchors := make(map[string]ast.Node)

	ast.Walk(&anchorVisitor{anchors: anchors}, node)

	return anchors
}

type anchorVisitor struct {
	anchors map[string]ast.Node
}

// Visit implements the [ast.Visitor] interface.
func (v *anchorVisitor) Visit(node ast.Node) ast.Visitor {
	if anchor, ok := node.(*ast.AnchorNode); ok {
		name := anchor.Name.String()
		v.anchors[name] = anchor.Value
	}

	return v
}

// resolveAliases resolves alias nodes using the anchor map.
func resolveAliases(node ast.Node, anchors map[string]ast.Node) ast.Node {
	if node == nil {
		return nil
	}

	alias, ok := node.(*ast.AliasNode)
	if !ok {
		return node
	}

	name := alias.Value.String()
	if resolved, found := anchors[name]; found {
		return resolved
	}

	// Unresolvable alias treated as null.
	return nil
}

// isBlank returns true if the byte slice contains only whitespace.
func isBlank(data []byte) bool {
	for _, b := range data {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return false
		}
	}

	return true
}
