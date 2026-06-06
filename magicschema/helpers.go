package magicschema

import (
	"encoding/json"
	"strings"

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

// ToSubSchema converts an arbitrary Go value (a map[string]any, bool, or any
// other JSON-marshalable value) to a [*jsonschema.Schema] by round-tripping
// through JSON. Returns nil for values that do not survive the round trip
// (annotation parse failures are skipped, never fatal).
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

// LastCommentGroup returns the lines of the final comment group: the lines
// after the last blank comment line, ignoring trailing blanks. A line is
// blank when stripping '#' markers and whitespace leaves nothing. Annotation
// formats scope to the comment group directly above a key, so earlier groups
// (often documentation for the file or a preceding key) are excluded. The
// returned slice contains no blank lines.
func LastCommentGroup(lines []string) []string {
	blank := func(line string) bool {
		stripped := strings.TrimSpace(line)
		stripped = strings.TrimLeft(stripped, "#")

		return strings.TrimSpace(stripped) == ""
	}

	end := len(lines)
	for end > 0 && blank(lines[end-1]) {
		end--
	}

	lastBlank := -1

	for i, line := range lines[:end] {
		if blank(line) {
			lastBlank = i
		}
	}

	return lines[lastBlank+1 : end]
}
