package jsonschema

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// Schema is an alias for the upstream [jsonschema.Schema] type, so callers can
// reference it without importing google/jsonschema-go directly.
type Schema = jsonschema.Schema

// Ptr returns a pointer to a new variable whose value is x.
func Ptr[T any](x T) *T { return &x }

// Raw marshals v with encoding/json for raw-JSON schema fields such as
// [Schema.Default].
func Raw(v any) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal raw value: %w", err)
	}

	return data, nil
}

// MustRaw is [Raw] but panics on marshal error; intended for values known
// valid at compile time.
func MustRaw(v any) json.RawMessage {
	data, err := Raw(v)
	if err != nil {
		panic(err)
	}

	return data
}

// IsTrueSchema reports whether s is the boolean true schema form: a schema
// with no fields set, which marshals to JSON true and accepts every
// instance. Annotation-only schemas (a description but no constraints)
// return false. Returns false for nil.
//
// The check is strict about JSON's nil-versus-empty distinction, matching
// the upstream Schema docs: a non-nil empty map or slice counts as set, so
// for example Schema{Enum: []any{}} — which vacuously rejects every
// instance — is not the true schema. The field enumeration below covers
// every exported Schema field; a maintenance test fails when an upstream
// addition is not classified.
func IsTrueSchema(s *Schema) bool {
	return s != nil &&
		// Core.
		s.ID == "" && s.Schema == "" && s.Ref == "" && s.Comment == "" &&
		s.Defs == nil && s.Definitions == nil &&
		s.DependencySchemas == nil && s.DependencyStrings == nil &&
		s.Anchor == "" && s.DynamicAnchor == "" && s.DynamicRef == "" &&
		s.Vocabulary == nil &&
		// Metadata.
		s.Title == "" && s.Description == "" && s.Default == nil &&
		!s.Deprecated && !s.ReadOnly && !s.WriteOnly &&
		s.Examples == nil &&
		// Validation.
		s.Type == "" && s.Types == nil && s.Enum == nil && s.Const == nil &&
		s.MultipleOf == nil &&
		s.Minimum == nil && s.Maximum == nil &&
		s.ExclusiveMinimum == nil && s.ExclusiveMaximum == nil &&
		s.MinLength == nil && s.MaxLength == nil && s.Pattern == "" &&
		// Arrays.
		s.PrefixItems == nil && s.Items == nil && s.ItemsArray == nil &&
		s.MinItems == nil && s.MaxItems == nil &&
		s.AdditionalItems == nil && !s.UniqueItems && s.Contains == nil &&
		s.MinContains == nil && s.MaxContains == nil &&
		s.UnevaluatedItems == nil &&
		// Objects.
		s.MinProperties == nil && s.MaxProperties == nil &&
		s.Required == nil && s.DependentRequired == nil &&
		s.Properties == nil && s.PatternProperties == nil &&
		s.AdditionalProperties == nil && s.PropertyNames == nil &&
		s.UnevaluatedProperties == nil &&
		// Logic.
		s.AllOf == nil && s.AnyOf == nil && s.OneOf == nil && s.Not == nil &&
		// Conditional.
		s.If == nil && s.Then == nil && s.Else == nil &&
		s.DependentSchemas == nil &&
		// Content, format, and extensions.
		s.ContentEncoding == "" && s.ContentMediaType == "" &&
		s.ContentSchema == nil && s.Format == "" &&
		s.Extra == nil && s.PropertyOrder == nil
}

// IsFalseSchema reports whether s is the boolean false schema form
// {"not": {}}, which marshals to JSON false and rejects every instance.
// Returns false for nil.
//
// This is the shape the upstream produces when unmarshaling the JSON
// boolean false: a Not pointing at a true schema (see [IsTrueSchema]) with
// every sibling field zero. Any sibling — including annotations such as a
// title — defeats the form, because the schema then marshals to an object
// rather than to false.
func IsFalseSchema(s *Schema) bool {
	if s == nil || s.Not == nil || !IsTrueSchema(s.Not) {
		return false
	}

	// A value copy shares sub-schema pointers with s, but IsTrueSchema reads
	// fields without mutating them, so clearing Not on the copy leaves s
	// untouched while letting the single field enumeration in IsTrueSchema
	// decide whether Not has siblings.
	rest := *s
	rest.Not = nil

	return IsTrueSchema(&rest)
}
