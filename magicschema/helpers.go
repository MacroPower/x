package magicschema

import (
	"encoding/json"

	"github.com/goccy/go-yaml"
	"github.com/google/jsonschema-go/jsonschema"
)

// DefaultValue converts a Go value to a [json.RawMessage] suitable for use
// as a JSON Schema default value. Returns nil if marshaling fails.
func DefaultValue(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}

	return b
}

// ConstValue converts a Go value to a pointer-to-any suitable for use
// as a JSON Schema const value.
func ConstValue(v any) *any {
	return jsonschema.Ptr(v)
}

// TrueSchema returns a schema that validates everything (marshals to JSON true).
func TrueSchema() *jsonschema.Schema {
	return &jsonschema.Schema{}
}

// FalseSchema returns a schema that validates nothing (marshals to JSON false).
func FalseSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Not: &jsonschema.Schema{}}
}

// ToSubSchema converts a map[string]any to a [*jsonschema.Schema] by marshaling
// through JSON.
func ToSubSchema(val any) *jsonschema.Schema {
	if val == nil {
		return nil
	}

	b, err := json.Marshal(val)
	if err != nil {
		return nil
	}

	var schema jsonschema.Schema

	err = json.Unmarshal(b, &schema)
	if err != nil {
		return nil
	}

	return &schema
}

// ToSubSchemaArray converts a []any to []*jsonschema.Schema.
func ToSubSchemaArray(val any) []*jsonschema.Schema {
	arr, ok := val.([]any)
	if !ok {
		return nil
	}

	schemas := make([]*jsonschema.Schema, 0, len(arr))

	for _, item := range arr {
		s := ToSubSchema(item)
		if s != nil {
			schemas = append(schemas, s)
		}
	}

	return schemas
}

// ToSubSchemaMap converts a map[string]any to map[string]*jsonschema.Schema.
func ToSubSchemaMap(val any) map[string]*jsonschema.Schema {
	m, ok := val.(map[string]any)
	if !ok {
		return nil
	}

	result := make(map[string]*jsonschema.Schema, len(m))

	for key, v := range m {
		s := ToSubSchema(v)
		if s != nil {
			result[key] = s
		}
	}

	return result
}

// ParseYAMLValue parses a YAML value string into [json.RawMessage].
func ParseYAMLValue(val string) []byte {
	var v any

	err := yaml.Unmarshal([]byte(val), &v)
	if err != nil {
		return nil
	}

	return DefaultValue(v)
}
