package jsonschema

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
)

// Schema is an alias for the upstream [jsonschema.Schema] type, so callers can
// reference it without importing google/jsonschema-go directly.
type Schema = jsonschema.Schema

// Raw marshals v with encoding/json for raw-JSON schema fields such as
// [Schema.Default].
func Raw(v any) (jsontext.Value, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal raw value: %w", err)
	}

	return data, nil
}

// MustRaw is [Raw] but panics on marshal error; intended for values known
// valid at compile time.
func MustRaw(v any) jsontext.Value {
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
// the upstream Schema docs: a non-nil empty map or slice counts as set. So
// Schema{Enum: []any{}}, for example, is not the true schema, even though it
// vacuously rejects every instance. The per-field zero check comes from the
// canonical [go.jacobcolvin.com/x/jsonschema/internal/schemafield] table,
// which covers every exported Schema field; a maintenance test there fails
// when an upstream addition is not classified.
func IsTrueSchema(s *Schema) bool {
	return schemafield.IsTrue(s)
}

// IsFalseSchema reports whether s is the boolean false schema form
// {"not": {}}, which marshals to JSON false and rejects every instance.
// Returns false for nil.
//
// This is the shape the upstream produces when unmarshaling the JSON
// boolean false: a Not pointing at a true schema (see [IsTrueSchema]) with
// every sibling field zero. Any sibling at all defeats the form, including
// annotations such as a title, because the schema then marshals to an object
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
