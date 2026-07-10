package magicschema

import (
	"maps"
	"slices"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"
)

// AnnotationResult wraps an Annotator's output with metadata that doesn't
// belong on the schema node itself.
type AnnotationResult struct {
	Schema      *jsonschema.Schema
	HasRequired *bool // non-nil if annotator explicitly set required (true or false)

	// FallbackDescription is prose the annotator inferred from surrounding
	// comments rather than parsed from an explicit annotation key (dadav
	// derives one from the comment lines around a @schema block). It fills
	// the merged schema's description only when no annotator of any priority
	// set one explicitly, so inferred prose never shadows an explicit
	// description annotation, while still outranking the generator's own
	// comment fallback.
	FallbackDescription string

	Skip            bool // true if this subtree should be omitted from the schema
	SkipProperties  bool // true to strip Properties from the output schema
	MergeProperties bool // true to merge child properties into additionalProperties
}

// Annotator extracts schema metadata from YAML comments.
// The node parameter is an ast.Node from github.com/goccy/go-yaml/ast,
// typically an *ast.MappingValueNode for key-value pairs.
// The keyPath is the dot-separated path (e.g., "image.repository").
//
// Annotator instances stored in a [Registry] act as stateless prototypes.
// [ForContent] returns a fresh, prepared clone for each input file, so
// the prototype is never mutated and is safe for concurrent reuse.
type Annotator interface {
	Name() string

	// ForContent returns a new Annotator prepared with the given file
	// content. The receiver acts as a stateless prototype and is never
	// mutated. Annotators that need file-level context (e.g., bitnami's
	// ## @param annotations) return a clone populated with parsed state.
	// Stateless annotators may return a new zero-value instance.
	// Called once per document of each input file, before any Annotate
	// calls for that document, so per-document state resets at document
	// boundaries.
	ForContent(content []byte) (Annotator, error)

	// Annotate extracts schema annotations from the given AST node.
	// Returns nil if the annotator does not recognize any annotations
	// on this node.
	Annotate(node ast.Node, keyPath string) *AnnotationResult
}

// RootAnnotator is an optional interface that annotators can implement to
// provide root-level schema properties (e.g., from @schema.root blocks).
// The Generator merges root schema fields into the final output before
// applying CLI-level overrides.
type RootAnnotator interface {
	RootSchema() *jsonschema.Schema
}

// MarkerAnnotator is an optional interface that annotators can implement to
// report which comment lines are annotation markers of their format. The
// fallback description extractor consults every prepared annotator that
// implements it, in addition to the built-in [IsAnnotationComment] list, so
// a custom format's marker lines never leak into property descriptions.
// Built-in formats need no recognizer: [IsAnnotationComment] always covers
// them, even when their annotator is not enabled.
type MarkerAnnotator interface {
	// IsAnnotationLine reports whether a comment line is an annotation
	// marker of this annotator's format rather than prose. The line arrives
	// with its leading "#" markers stripped and surrounding whitespace
	// trimmed.
	IsAnnotationLine(line string) bool
}

// mergeAnnotations merges multiple AnnotationResults in priority order
// (first element has highest priority). Returns nil if all inputs are nil.
// The merged schema is a copy, so downstream mutation never reaches into a
// schema owned by an annotator.
func mergeAnnotations(results []*AnnotationResult) *AnnotationResult {
	var merged *AnnotationResult

	for _, r := range results {
		if r == nil {
			continue
		}

		if merged == nil {
			// Copy every field, then replace Schema with a copy so downstream
			// mutation never reaches the annotator's. Copying the whole struct
			// keeps a new AnnotationResult field carried by the highest-priority
			// result without a separate edit here.
			clone := *r
			clone.Schema = copySchema(r.Schema)
			merged = &clone

			continue
		}

		// Skip, SkipProperties, MergeProperties are OR'd.
		merged.Skip = merged.Skip || r.Skip
		merged.SkipProperties = merged.SkipProperties || r.SkipProperties
		merged.MergeProperties = merged.MergeProperties || r.MergeProperties

		// HasRequired: highest-priority annotator that explicitly sets it wins.
		if merged.HasRequired == nil && r.HasRequired != nil {
			merged.HasRequired = r.HasRequired
		}

		// FallbackDescription: first non-empty in priority order, applied to
		// the schema only after the fold so an explicit description from ANY
		// priority outranks inferred prose.
		if merged.FallbackDescription == "" {
			merged.FallbackDescription = r.FallbackDescription
		}

		if r.Schema == nil {
			continue
		}

		if merged.Schema == nil {
			merged.Schema = copySchema(r.Schema)

			continue
		}

		// Merge schema fields: lower priority fills in gaps.
		mergeSchemaFields(merged.Schema, r.Schema)
	}

	// The inferred prose fills in last: every annotator's explicit
	// Description has had its chance to win the field above.
	if merged != nil && merged.Schema != nil && merged.Schema.Description == "" {
		merged.Schema.Description = merged.FallbackDescription
	}

	return merged
}

// copySchema returns a shallow copy of s with its own Extra map and Required
// slice. The copy supports the mutations the build pipeline performs -- field
// reassignment, writes into Extra, and appends to Required (via addRequired in
// fillObjectFromStructure) -- without reaching the original. All other map,
// slice, and sub-schema fields still alias the original, so downstream code
// must only reassign those fields on the copy, never write into their contents.
func copySchema(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}

	c := *s

	// A non-nil but empty Types slice survives the shallow copy and later
	// collides with a Type that structural inference fills, leaving both set --
	// a combination the jsonschema marshaler rejects, failing the whole
	// document's marshal. Normalize it to nil, mirroring SetSchemaType's
	// empty-list handling, so inference can fill Type cleanly. The in-package
	// producers never emit an empty Types (SetSchemaType leaves an empty list
	// unset and ToSubSchema normalizes its round-tripped tree), so this guard
	// and its mergeSchemaFields counterpart are defense-in-depth for external
	// Annotator implementations that hand-construct one.
	if len(c.Types) == 0 {
		c.Types = nil
	}

	if s.Extra != nil {
		c.Extra = maps.Clone(s.Extra)
	}

	// Required is the one slice the pipeline appends to; clone it so an append
	// with spare capacity never writes into the annotator's backing array.
	if s.Required != nil {
		c.Required = slices.Clone(s.Required)
	}

	return &c
}

// mergeSchemaFields merges fields from src into dst where dst has zero values.
// Dst has higher priority; src fills in gaps.
func mergeSchemaFields(dst, src *jsonschema.Schema) {
	// A higher-priority $ref annotation is emitted as authored -- the
	// contract buildChildSchema's Ref early-return and applyRootAnnotations
	// enforce for the structural side. Filling lower-priority siblings in
	// beside it would be inert under strict Draft 7 (siblings of $ref are
	// ignored) and would double-constrain against the referent under
	// validators that do apply siblings (fail closed), so no gap fill runs
	// at all.
	if dst.Ref != "" {
		return
	}

	// Under Draft 7 every validation sibling of $ref is ignored, so grafting
	// a lower-priority $ref beside higher-priority constraints would let the
	// reference govern entirely and silently disable what the winner wrote
	// (fail closed, and the inverse of the documented precedence) -- the same
	// reason applyRootAnnotations strips structural keywords beside a root
	// $ref. The fill is allowed only when dst carries no type and no value
	// constraint for the reference to nullify, judged before this call's own
	// gap fills add src's other fields to dst.
	refFillOK := len(typeList(dst)) == 0 && !constrainsValue(dst)

	// Fill the type from the lower-priority src only when src actually carries
	// one and it does not contradict a value set the higher-priority dst already
	// carries. Requiring a real src type keeps a non-nil but empty src.Types from
	// being grafted on: it would emit an invalid "type": [] and, once structural
	// inference fills Type, set both Type and Types and break the document's
	// final marshal. In-package producers never emit an empty Types (SetSchemaType
	// and ToSubSchema both normalize it away), so this clause, like the copySchema
	// guard, is defense-in-depth for external Annotator implementations.
	// Grafting type:string onto an existing const:5 (or an integer
	// enum) would instead leave a schema no value satisfies (fail closed) -- the
	// mirror of the enum/const fill guard below.
	if dst.Type == "" && len(dst.Types) == 0 &&
		(src.Type != "" || len(src.Types) > 0) &&
		valueSetFitsType(enumValues(dst), &jsonschema.Schema{Type: src.Type, Types: src.Types}) {
		dst.Type = src.Type
		dst.Types = src.Types
	}

	if dst.Title == "" {
		dst.Title = src.Title
	}

	if dst.Description == "" {
		dst.Description = src.Description
	}

	if dst.Default == nil {
		dst.Default = src.Default
	}

	// Enum and const are one value-set constraint (a const is a single-value
	// enum), so they fill as a unit. If dst already constrains the value set,
	// the higher-priority annotator wins outright; filling them independently
	// could leave a higher-priority enum beside a lower-priority const, which
	// AND-combine to reject every value (fail closed). The fill is likewise
	// skipped when the value set contradicts dst's resolved type: grafting a
	// lower-priority const or enum onto a higher-priority incompatible type
	// would reject every value just the same.
	if dst.Enum == nil && dst.Const == nil && valueSetFitsType(enumValues(src), dst) {
		dst.Enum = src.Enum
		dst.Const = src.Const
	}

	if dst.Pattern == "" {
		dst.Pattern = src.Pattern
	}

	if dst.Format == "" {
		dst.Format = src.Format
	}

	if dst.Minimum == nil {
		dst.Minimum = src.Minimum
	}

	if dst.Maximum == nil {
		dst.Maximum = src.Maximum
	}

	if dst.ExclusiveMinimum == nil {
		dst.ExclusiveMinimum = src.ExclusiveMinimum
	}

	if dst.ExclusiveMaximum == nil {
		dst.ExclusiveMaximum = src.ExclusiveMaximum
	}

	if dst.MultipleOf == nil {
		dst.MultipleOf = src.MultipleOf
	}

	if dst.MinLength == nil {
		dst.MinLength = src.MinLength
	}

	if dst.MaxLength == nil {
		dst.MaxLength = src.MaxLength
	}

	if dst.MinItems == nil {
		dst.MinItems = src.MinItems
	}

	if dst.MaxItems == nil {
		dst.MaxItems = src.MaxItems
	}

	if !dst.UniqueItems {
		dst.UniqueItems = src.UniqueItems
	}

	if dst.MinProperties == nil {
		dst.MinProperties = src.MinProperties
	}

	if dst.MaxProperties == nil {
		dst.MaxProperties = src.MaxProperties
	}

	// Items (single-schema array form) and ItemsArray (tuple form) are the two
	// mutually-exclusive shapes of the items keyword; jsonschema's basicChecks
	// rejects a schema carrying both, which would fail the whole document's
	// final marshal. Fill them as a unit so a higher-priority dst shape is never
	// crossed with a lower-priority src's other shape, mirroring the Type/Types
	// and Enum/Const unit guards above.
	if dst.Items == nil && dst.ItemsArray == nil {
		dst.Items = src.Items
		dst.ItemsArray = src.ItemsArray
	}

	if dst.Properties == nil {
		dst.Properties = src.Properties
		dst.PropertyOrder = src.PropertyOrder
	}

	if dst.AdditionalProperties == nil {
		dst.AdditionalProperties = src.AdditionalProperties
	}

	if dst.PatternProperties == nil {
		dst.PatternProperties = src.PatternProperties
	}

	if dst.PropertyNames == nil {
		dst.PropertyNames = src.PropertyNames
	}

	// Clone rather than alias: addRequired appends to the merged schema's
	// Required downstream (via fillObjectFromStructure), and a lower-priority
	// annotator may return a shared prototype schema whose backing array must
	// not be written through -- the same contract copySchema upholds.
	if dst.Required == nil {
		dst.Required = slices.Clone(src.Required)
	}

	if dst.AllOf == nil {
		dst.AllOf = src.AllOf
	}

	if dst.AnyOf == nil {
		dst.AnyOf = src.AnyOf
	}

	if dst.OneOf == nil {
		dst.OneOf = src.OneOf
	}

	if dst.Not == nil {
		dst.Not = src.Not
	}

	// The if/then/else conditional only has meaning as a unit -- the same
	// rule mergeSchemas applies. A then or else without an if is inert, so
	// filling the trio independently could graft a lower-priority if under a
	// higher-priority inert then and activate a conditional neither annotator
	// wrote (fail closed). Fill only when dst carries none of the three.
	if dst.If == nil && dst.Then == nil && dst.Else == nil {
		dst.If = src.If
		dst.Then = src.Then
		dst.Else = src.Else
	}

	if !dst.Deprecated {
		dst.Deprecated = src.Deprecated
	}

	if !dst.ReadOnly {
		dst.ReadOnly = src.ReadOnly
	}

	if !dst.WriteOnly {
		dst.WriteOnly = src.WriteOnly
	}

	if dst.Examples == nil {
		dst.Examples = src.Examples
	}

	if refFillOK {
		dst.Ref = src.Ref
	}

	if dst.ID == "" {
		dst.ID = src.ID
	}

	if dst.Comment == "" {
		dst.Comment = src.Comment
	}

	if dst.Schema == "" {
		dst.Schema = src.Schema
	}

	if dst.Anchor == "" {
		dst.Anchor = src.Anchor
	}

	if dst.DynamicAnchor == "" {
		dst.DynamicAnchor = src.DynamicAnchor
	}

	if dst.DynamicRef == "" {
		dst.DynamicRef = src.DynamicRef
	}

	if dst.Vocabulary == nil {
		dst.Vocabulary = src.Vocabulary
	}

	if dst.ContentEncoding == "" {
		dst.ContentEncoding = src.ContentEncoding
	}

	if dst.ContentMediaType == "" {
		dst.ContentMediaType = src.ContentMediaType
	}

	if dst.Contains == nil {
		dst.Contains = src.Contains
	}

	if dst.AdditionalItems == nil {
		dst.AdditionalItems = src.AdditionalItems
	}

	// $defs and definitions are mutually exclusive in the jsonschema-go model
	// (basicChecks rejects a schema with both set, which would break the
	// document's marshal), so fill them together only when dst constrains
	// neither -- the same unit-fill contract used for Type/Types above.
	if dst.Defs == nil && dst.Definitions == nil {
		dst.Defs = src.Defs
		dst.Definitions = src.Definitions
	}

	mergeDependencies(dst, src)

	if dst.UnevaluatedProperties == nil {
		dst.UnevaluatedProperties = src.UnevaluatedProperties
	}

	if dst.UnevaluatedItems == nil {
		dst.UnevaluatedItems = src.UnevaluatedItems
	}

	if dst.PrefixItems == nil {
		dst.PrefixItems = src.PrefixItems
	}

	if dst.MinContains == nil {
		dst.MinContains = src.MinContains
	}

	if dst.MaxContains == nil {
		dst.MaxContains = src.MaxContains
	}

	if dst.DependentRequired == nil {
		dst.DependentRequired = src.DependentRequired
	}

	if dst.DependentSchemas == nil {
		dst.DependentSchemas = src.DependentSchemas
	}

	if dst.ContentSchema == nil {
		dst.ContentSchema = src.ContentSchema
	}

	dst.Extra = mergeExtraInto(dst.Extra, src.Extra)
}

// mergeDependencies fills dst's dependency maps from src while preserving the
// invariant that no key appears in both DependencySchemas and
// DependencyStrings. The two maps are the schema and string-array shapes of a
// single dependencies key, and jsonschema's basicChecks rejects a key present
// in both, which would fail the whole document's marshal. The higher-priority
// dst keeps every key it already defines in either shape; a src key fills a gap
// only when dst constrains it in neither map. A src map is never mutated, since
// a lower-priority annotator may return a shared prototype schema.
func mergeDependencies(dst, src *jsonschema.Schema) {
	if src.DependencySchemas == nil && src.DependencyStrings == nil {
		return
	}

	claimed := func(key string) bool {
		if _, ok := dst.DependencySchemas[key]; ok {
			return true
		}

		_, ok := dst.DependencyStrings[key]

		return ok
	}

	// Per copySchema, dst's maps alias the annotator's prototype, so each map is
	// cloned before its first insert (clone-on-write) rather than mutated in
	// place. A nil map clones to nil, so a fresh map is allocated when dst had
	// none.
	schemasCloned := false

	for key, schema := range src.DependencySchemas {
		if claimed(key) {
			continue
		}

		if !schemasCloned {
			dst.DependencySchemas = maps.Clone(dst.DependencySchemas)
			if dst.DependencySchemas == nil {
				dst.DependencySchemas = make(map[string]*jsonschema.Schema)
			}

			schemasCloned = true
		}

		dst.DependencySchemas[key] = schema
	}

	stringsCloned := false

	for key, strs := range src.DependencyStrings {
		if claimed(key) {
			continue
		}

		if !stringsCloned {
			dst.DependencyStrings = maps.Clone(dst.DependencyStrings)
			if dst.DependencyStrings == nil {
				dst.DependencyStrings = make(map[string][]string)
			}

			stringsCloned = true
		}

		dst.DependencyStrings[key] = strs
	}
}
