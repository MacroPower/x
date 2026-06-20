package normalize

import (
	"encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// Accepted reports whether instance is one of the JSON-compatible Go types the
// validation walk works with: map[string]any, []any, string, float64,
// [json.Number], bool, or nil. Callers run [Value] first, so Go integer kinds
// and float32 are already converted by the time this runs. Other types, notably
// Go structs and [time.Time], are not accepted, because encoding/json does not
// produce them when unmarshaling into an any. The check recurses into
// containers, since [Value] leaves a nested non-JSON leaf (a struct, channel, or
// function inside a slice or map) unchanged; rejecting it here turns what would
// be a panic or a silent mis-validation deeper in the walk into a rejected
// instance.
func Accepted(instance any) bool {
	switch v := instance.(type) {
	case nil, bool, string, float64, json.Number:
		return true

	case []any:
		for _, item := range v {
			if !Accepted(item) {
				return false
			}
		}

		return true

	case map[string]any:
		for _, item := range v {
			if !Accepted(item) {
				return false
			}
		}

		return true

	default:
		return false
	}
}

// TypeName returns the JSON Schema type name for a normalized Go value, or ""
// for a value that has no JSON Schema type.
func TypeName(v any) string {
	if v == nil {
		return typename.Null
	}

	switch val := v.(type) {
	case bool:
		return typename.Boolean
	case string:
		return typename.String
	case json.Number, float64:
		if numrat.IsIntegralInstance(val) {
			return typename.Integer
		}

		return typename.Number

	case map[string]any:
		return typename.Object
	case []any:
		return typename.Array
	default:
		return ""
	}
}

// MatchesType reports whether a normalized instance matches the JSON Schema type
// name typ.
func MatchesType(instance any, typ string) bool {
	switch typ {
	case typename.Null:
		return instance == nil
	case typename.Boolean:
		_, ok := instance.(bool)
		return ok

	case typename.String:
		// Json.Number is a distinct type, so a string assertion already
		// excludes it; no separate numeric guard is needed.
		_, isStr := instance.(string)

		return isStr

	case typename.Integer:
		return numrat.IsIntegralInstance(instance)

	case typename.Number:
		switch instance.(type) {
		case float64, json.Number:
			return true
		}

		return false

	case typename.Object:
		_, ok := instance.(map[string]any)
		return ok

	case typename.Array:
		_, ok := instance.([]any)
		return ok
	}

	return false
}
