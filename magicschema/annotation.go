package magicschema

import (
	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"
)

// AnnotationResult wraps an Annotator's output with metadata that doesn't
// belong on the schema node itself.
type AnnotationResult struct {
	Schema          *jsonschema.Schema
	HasRequired     *bool // non-nil if annotator explicitly set required (true or false)
	Skip            bool  // true if this subtree should be omitted from the schema
	SkipProperties  bool  // true to strip Properties from the output schema
	MergeProperties bool  // true to merge child properties into additionalProperties
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
	// Called once per input file, before any Annotate calls for that file.
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

// mergeAnnotations merges multiple AnnotationResults in priority order
// (first element has highest priority). Returns nil if all inputs are nil.
func mergeAnnotations(results []*AnnotationResult) *AnnotationResult {
	var merged *AnnotationResult

	for _, r := range results {
		if r == nil {
			continue
		}

		if merged == nil {
			merged = &AnnotationResult{
				Schema:          r.Schema,
				HasRequired:     r.HasRequired,
				Skip:            r.Skip,
				SkipProperties:  r.SkipProperties,
				MergeProperties: r.MergeProperties,
			}

			continue
		}

		// Skip, SkipProperties, MergeProperties are OR'd.
		if r.Skip {
			merged.Skip = true
		}

		if r.SkipProperties {
			merged.SkipProperties = true
		}

		if r.MergeProperties {
			merged.MergeProperties = true
		}

		// HasRequired: highest-priority annotator that explicitly sets it wins.
		if merged.HasRequired == nil && r.HasRequired != nil {
			merged.HasRequired = r.HasRequired
		}

		if r.Schema == nil {
			continue
		}

		if merged.Schema == nil {
			merged.Schema = r.Schema

			continue
		}

		// Merge schema fields: lower priority fills in gaps.
		mergeSchemaFields(merged.Schema, r.Schema)
	}

	return merged
}

// mergeSchemaFields merges fields from src into dst where dst has zero values.
// Dst has higher priority; src fills in gaps.
func mergeSchemaFields(dst, src *jsonschema.Schema) {
	if dst.Type == "" && len(dst.Types) == 0 {
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

	if dst.Enum == nil {
		dst.Enum = src.Enum
	}

	if dst.Const == nil {
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

	if dst.Items == nil {
		dst.Items = src.Items
	}

	if dst.Properties == nil {
		dst.Properties = src.Properties
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

	if dst.Required == nil {
		dst.Required = src.Required
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

	if dst.If == nil {
		dst.If = src.If
	}

	if dst.Then == nil {
		dst.Then = src.Then
	}

	if dst.Else == nil {
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

	if dst.Ref == "" {
		dst.Ref = src.Ref
	}

	if dst.ID == "" {
		dst.ID = src.ID
	}

	if dst.Comment == "" {
		dst.Comment = src.Comment
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

	if dst.Definitions == nil {
		dst.Definitions = src.Definitions
	}

	if dst.Defs == nil {
		dst.Defs = src.Defs
	}

	if dst.DependencySchemas == nil {
		dst.DependencySchemas = src.DependencySchemas
	}

	if dst.DependencyStrings == nil {
		dst.DependencyStrings = src.DependencyStrings
	}

	if dst.UnevaluatedProperties == nil {
		dst.UnevaluatedProperties = src.UnevaluatedProperties
	}

	// Merge Extra maps.
	if src.Extra != nil {
		if dst.Extra == nil {
			dst.Extra = make(map[string]any)
		}

		for k, v := range src.Extra {
			if _, exists := dst.Extra[k]; !exists {
				dst.Extra[k] = v
			}
		}
	}
}
