package normalize

import (
	"encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

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
