// Package typename holds the canonical JSON Schema type-name constants and the
// predicate that recognizes them. The reflection generator (which assigns these
// to schema type fields) and the validator (which classifies instance types)
// live in separate packages, and internal consumers like schemashape inspect
// the same names, so the constants are centralized here to keep a single source
// of truth.
package typename

import "errors"

// ErrInvalidType reports a type name outside the seven JSON Schema type
// names. It is the one sentinel every type-name check mints, whether the
// name came from a struct tag or a schema document, so the parent package
// re-exports it once and [errors.Is] matches it from either origin.
var ErrInvalidType = errors.New("invalid type name")

// The seven JSON Schema type names.
const (
	Null    = "null"
	Boolean = "boolean"
	String  = "string"
	Integer = "integer"
	Number  = "number"
	Object  = "object"
	Array   = "array"
)

// Valid reports whether s is one of the seven JSON Schema type names.
func Valid(s string) bool {
	switch s {
	case Null, Boolean, String, Integer, Number, Object, Array:
		return true
	default:
		return false
	}
}
