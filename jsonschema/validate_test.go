package jsonschema_test

import (
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"empty schema accepts anything": {
			schema:   &jsonschema.Schema{},
			instance: "hello",
		},
		"empty schema accepts null": {
			schema:   &jsonschema.Schema{},
			instance: nil,
		},
		"empty schema accepts object": {
			schema:   &jsonschema.Schema{},
			instance: map[string]any{"a": 1.0},
		},
		"false schema rejects everything": {
			schema:   &jsonschema.Schema{Not: &jsonschema.Schema{}},
			instance: "hello",
			err:      "value is not allowed",
		},
		"type string accepts string": {
			schema:   &jsonschema.Schema{Type: "string"},
			instance: "hello",
		},
		"type string rejects number": {
			schema:   &jsonschema.Schema{Type: "string"},
			instance: 42.0,
			err:      `(type): expected "string", got "integer"`,
		},
		"type integer accepts integer": {
			schema:   &jsonschema.Schema{Type: "integer"},
			instance: 42.0,
		},
		"type integer rejects float": {
			schema:   &jsonschema.Schema{Type: "integer"},
			instance: 3.14,
			err:      `(type): expected "integer", got "number"`,
		},
		"type number accepts float": {
			schema:   &jsonschema.Schema{Type: "number"},
			instance: 3.14,
		},
		"type number accepts integer": {
			schema:   &jsonschema.Schema{Type: "number"},
			instance: 42.0,
		},
		"type boolean accepts true": {
			schema:   &jsonschema.Schema{Type: "boolean"},
			instance: true,
		},
		"type boolean rejects string": {
			schema:   &jsonschema.Schema{Type: "boolean"},
			instance: "true",
			err:      `(type): expected "boolean", got "string"`,
		},
		"type null accepts nil": {
			schema:   &jsonschema.Schema{Type: "null"},
			instance: nil,
		},
		"type null rejects string": {
			schema:   &jsonschema.Schema{Type: "null"},
			instance: "null",
			err:      `(type): expected "null", got "string"`,
		},
		"type array accepts array": {
			schema:   &jsonschema.Schema{Type: "array"},
			instance: []any{1.0, 2.0},
		},
		"type object accepts object": {
			schema:   &jsonschema.Schema{Type: "object"},
			instance: map[string]any{},
		},
		"multiple types accept any matching": {
			schema:   &jsonschema.Schema{Types: []string{"string", "null"}},
			instance: nil,
		},
		"multiple types reject non-matching": {
			schema:   &jsonschema.Schema{Types: []string{"string", "null"}},
			instance: 42.0,
			err:      `(type)`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateEnum(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"enum match": {
			schema:   &jsonschema.Schema{Enum: []any{"a", "b", "c"}},
			instance: "b",
		},
		"enum no match": {
			schema:   &jsonschema.Schema{Enum: []any{"a", "b", "c"}},
			instance: "d",
			err:      "(enum)",
		},
		"enum numeric equality": {
			schema:   &jsonschema.Schema{Enum: []any{1.0, 2.0}},
			instance: json.Number("1"),
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateConst(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"const match": {
			schema:   &jsonschema.Schema{Const: jsonschema.Ptr(any("hello"))},
			instance: "hello",
		},
		"const no match": {
			schema:   &jsonschema.Schema{Const: jsonschema.Ptr(any("hello"))},
			instance: "world",
			err:      "(const)",
		},
		"const null": {
			schema:   &jsonschema.Schema{Const: jsonschema.Ptr(any(nil))},
			instance: nil,
		},
		"const null rejects non-null": {
			schema:   &jsonschema.Schema{Const: jsonschema.Ptr(any(nil))},
			instance: "hello",
			err:      "(const)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateNumeric(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"minimum pass": {
			schema:   &jsonschema.Schema{Type: "number", Minimum: jsonschema.Ptr(0.0)},
			instance: 5.0,
		},
		"minimum fail": {
			schema:   &jsonschema.Schema{Type: "number", Minimum: jsonschema.Ptr(0.0)},
			instance: -1.0,
			err:      "(minimum)",
		},
		"minimum boundary": {
			schema:   &jsonschema.Schema{Type: "number", Minimum: jsonschema.Ptr(0.0)},
			instance: 0.0,
		},
		"maximum pass": {
			schema:   &jsonschema.Schema{Type: "number", Maximum: jsonschema.Ptr(100.0)},
			instance: 50.0,
		},
		"maximum fail": {
			schema:   &jsonschema.Schema{Type: "number", Maximum: jsonschema.Ptr(100.0)},
			instance: 101.0,
			err:      "(maximum)",
		},
		"exclusiveMinimum pass": {
			schema:   &jsonschema.Schema{Type: "number", ExclusiveMinimum: jsonschema.Ptr(0.0)},
			instance: 0.1,
		},
		"exclusiveMinimum fail at boundary": {
			schema:   &jsonschema.Schema{Type: "number", ExclusiveMinimum: jsonschema.Ptr(0.0)},
			instance: 0.0,
			err:      "(exclusiveMinimum)",
		},
		"exclusiveMaximum pass": {
			schema:   &jsonschema.Schema{Type: "number", ExclusiveMaximum: jsonschema.Ptr(100.0)},
			instance: 99.0,
		},
		"exclusiveMaximum fail at boundary": {
			schema:   &jsonschema.Schema{Type: "number", ExclusiveMaximum: jsonschema.Ptr(100.0)},
			instance: 100.0,
			err:      "(exclusiveMaximum)",
		},
		"multipleOf pass": {
			schema:   &jsonschema.Schema{Type: "number", MultipleOf: jsonschema.Ptr(3.0)},
			instance: 9.0,
		},
		"multipleOf fail": {
			schema:   &jsonschema.Schema{Type: "number", MultipleOf: jsonschema.Ptr(3.0)},
			instance: 10.0,
			err:      "(multipleOf)",
		},
		"json.Number integer": {
			schema:   &jsonschema.Schema{Type: "integer", Minimum: jsonschema.Ptr(0.0)},
			instance: json.Number("5"),
		},
		"json.Number negative fail minimum": {
			schema:   &jsonschema.Schema{Type: "integer", Minimum: jsonschema.Ptr(0.0)},
			instance: json.Number("-1"),
			err:      "(minimum)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateString(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"minLength pass": {
			schema:   &jsonschema.Schema{Type: "string", MinLength: jsonschema.Ptr(3)},
			instance: "abc",
		},
		"minLength fail": {
			schema:   &jsonschema.Schema{Type: "string", MinLength: jsonschema.Ptr(3)},
			instance: "ab",
			err:      "(minLength)",
		},
		"maxLength pass": {
			schema:   &jsonschema.Schema{Type: "string", MaxLength: jsonschema.Ptr(5)},
			instance: "hello",
		},
		"maxLength fail": {
			schema:   &jsonschema.Schema{Type: "string", MaxLength: jsonschema.Ptr(5)},
			instance: "toolong",
			err:      "(maxLength)",
		},
		"pattern pass": {
			schema:   &jsonschema.Schema{Type: "string", Pattern: `^\d+$`},
			instance: "123",
		},
		"pattern fail": {
			schema:   &jsonschema.Schema{Type: "string", Pattern: `^\d+$`},
			instance: "abc",
			err:      "(pattern)",
		},
		// Draft-07 asserts format by default; 2020-12 is annotation-only.
		"format date-time pass": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Type:   "string",
				Format: "date-time",
			},
			instance: "2024-01-01T00:00:00Z",
		},
		"format date-time fail": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Type:   "string",
				Format: "date-time",
			},
			instance: "not-a-date",
			err:      "(format)",
		},
		"unicode length": {
			schema:   &jsonschema.Schema{Type: "string", MinLength: jsonschema.Ptr(3)},
			instance: "\u00e9\u00e9\u00e9",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateArray(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"items pass": {
			schema:   &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "integer"}},
			instance: []any{1.0, 2.0, 3.0},
		},
		"items fail": {
			schema:   &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "integer"}},
			instance: []any{1.0, "two", 3.0},
			err:      "(type)",
		},
		"minItems pass": {
			schema:   &jsonschema.Schema{Type: "array", MinItems: jsonschema.Ptr(2)},
			instance: []any{1.0, 2.0},
		},
		"minItems fail": {
			schema:   &jsonschema.Schema{Type: "array", MinItems: jsonschema.Ptr(2)},
			instance: []any{1.0},
			err:      "(minItems)",
		},
		"maxItems pass": {
			schema:   &jsonschema.Schema{Type: "array", MaxItems: jsonschema.Ptr(3)},
			instance: []any{1.0, 2.0},
		},
		"maxItems fail": {
			schema:   &jsonschema.Schema{Type: "array", MaxItems: jsonschema.Ptr(1)},
			instance: []any{1.0, 2.0},
			err:      "(maxItems)",
		},
		"uniqueItems pass": {
			schema:   &jsonschema.Schema{Type: "array", UniqueItems: true},
			instance: []any{1.0, 2.0, 3.0},
		},
		"uniqueItems fail": {
			schema:   &jsonschema.Schema{Type: "array", UniqueItems: true},
			instance: []any{1.0, 2.0, 1.0},
			err:      "(uniqueItems)",
		},
		"uniqueItems json.Number equality": {
			schema:   &jsonschema.Schema{Type: "array", UniqueItems: true},
			instance: []any{json.Number("1"), 1.0},
			err:      "(uniqueItems)",
		},
		"contains pass": {
			schema:   &jsonschema.Schema{Type: "array", Contains: &jsonschema.Schema{Type: "string"}},
			instance: []any{1.0, "hello", 3.0},
		},
		"contains fail": {
			schema:   &jsonschema.Schema{Type: "array", Contains: &jsonschema.Schema{Type: "string"}},
			instance: []any{1.0, 2.0, 3.0},
			err:      "(contains)",
		},
		"contains minContains 0": {
			schema: &jsonschema.Schema{
				Type:        "array",
				Contains:    &jsonschema.Schema{Type: "string"},
				MinContains: jsonschema.Ptr(0),
			},
			instance: []any{1.0, 2.0, 3.0},
		},
		"prefixItems 2020-12": {
			schema: &jsonschema.Schema{
				Schema:      "https://json-schema.org/draft/2020-12/schema",
				Type:        "array",
				PrefixItems: []*jsonschema.Schema{{Type: "string"}, {Type: "integer"}},
			},
			instance: []any{"hello", 42.0},
		},
		"prefixItems fail": {
			schema: &jsonschema.Schema{
				Schema:      "https://json-schema.org/draft/2020-12/schema",
				Type:        "array",
				PrefixItems: []*jsonschema.Schema{{Type: "string"}, {Type: "integer"}},
			},
			instance: []any{42.0, "hello"},
			err:      "(type)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateObject(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"properties pass": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
					"age":  {Type: "integer"},
				},
			},
			instance: map[string]any{"name": "Alice", "age": 30.0},
		},
		"properties type fail": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
				},
			},
			instance: map[string]any{"name": 123.0},
			err:      "(type)",
		},
		"required pass": {
			schema: &jsonschema.Schema{
				Type:     "object",
				Required: []string{"name"},
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
				},
			},
			instance: map[string]any{"name": "Alice"},
		},
		"required fail": {
			schema: &jsonschema.Schema{
				Type:     "object",
				Required: []string{"name"},
			},
			instance: map[string]any{},
			err:      "(required)",
		},
		"additionalProperties false": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
				},
				AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			},
			instance: map[string]any{"name": "Alice", "extra": "field"},
			err:      "value is not allowed",
		},
		"additionalProperties schema": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
				},
				AdditionalProperties: &jsonschema.Schema{Type: "integer"},
			},
			instance: map[string]any{"name": "Alice", "extra": 42.0},
		},
		"minProperties pass": {
			schema:   &jsonschema.Schema{Type: "object", MinProperties: jsonschema.Ptr(1)},
			instance: map[string]any{"a": 1.0},
		},
		"minProperties fail": {
			schema:   &jsonschema.Schema{Type: "object", MinProperties: jsonschema.Ptr(1)},
			instance: map[string]any{},
			err:      "(minProperties)",
		},
		"maxProperties pass": {
			schema:   &jsonschema.Schema{Type: "object", MaxProperties: jsonschema.Ptr(2)},
			instance: map[string]any{"a": 1.0, "b": 2.0},
		},
		"maxProperties fail": {
			schema:   &jsonschema.Schema{Type: "object", MaxProperties: jsonschema.Ptr(1)},
			instance: map[string]any{"a": 1.0, "b": 2.0},
			err:      "(maxProperties)",
		},
		"patternProperties pass": {
			schema: &jsonschema.Schema{
				Type: "object",
				PatternProperties: map[string]*jsonschema.Schema{
					"^S_": {Type: "string"},
				},
			},
			instance: map[string]any{"S_name": "Alice"},
		},
		"patternProperties fail": {
			schema: &jsonschema.Schema{
				Type: "object",
				PatternProperties: map[string]*jsonschema.Schema{
					"^S_": {Type: "string"},
				},
			},
			instance: map[string]any{"S_name": 42.0},
			err:      "(type)",
		},
		"propertyNames pass": {
			schema: &jsonschema.Schema{
				Type:          "object",
				PropertyNames: &jsonschema.Schema{MaxLength: jsonschema.Ptr(3)},
			},
			instance: map[string]any{"foo": 1.0, "bar": 2.0},
		},
		"propertyNames fail": {
			schema: &jsonschema.Schema{
				Type:          "object",
				PropertyNames: &jsonschema.Schema{MaxLength: jsonschema.Ptr(3)},
			},
			instance: map[string]any{"toolong": 1.0},
			err:      "(maxLength)",
		},
		"dependentRequired 2020-12": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependentRequired: map[string][]string{
					"bar": {"foo"},
				},
			},
			instance: map[string]any{"bar": 1.0, "foo": 2.0},
		},
		"dependentRequired fail": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependentRequired: map[string][]string{
					"bar": {"foo"},
				},
			},
			instance: map[string]any{"bar": 1.0},
			err:      "(dependentRequired)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateComposition(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"allOf pass": {
			schema: &jsonschema.Schema{
				AllOf: []*jsonschema.Schema{
					{Type: "number"},
					{Minimum: jsonschema.Ptr(0.0)},
				},
			},
			instance: 5.0,
		},
		"allOf fail one": {
			schema: &jsonschema.Schema{
				AllOf: []*jsonschema.Schema{
					{Type: "number"},
					{Minimum: jsonschema.Ptr(10.0)},
				},
			},
			instance: 5.0,
			err:      "(allOf)",
		},
		"anyOf pass": {
			schema: &jsonschema.Schema{
				AnyOf: []*jsonschema.Schema{
					{Type: "string"},
					{Type: "integer"},
				},
			},
			instance: "hello",
		},
		"anyOf fail": {
			schema: &jsonschema.Schema{
				AnyOf: []*jsonschema.Schema{
					{Type: "string"},
					{Type: "integer"},
				},
			},
			instance: true,
			err:      "(anyOf)",
		},
		"oneOf pass": {
			schema: &jsonschema.Schema{
				OneOf: []*jsonschema.Schema{
					{Type: "string"},
					{Type: "integer"},
				},
			},
			instance: "hello",
		},
		"oneOf fail none": {
			schema: &jsonschema.Schema{
				OneOf: []*jsonschema.Schema{
					{Type: "string"},
					{Type: "integer"},
				},
			},
			instance: true,
			err:      "(oneOf)",
		},
		"oneOf fail multiple": {
			schema: &jsonschema.Schema{
				OneOf: []*jsonschema.Schema{
					{Type: "number"},
					{Type: "integer"},
				},
			},
			instance: 42.0,
			err:      "validated against 2 subschemas",
		},
		"not pass": {
			schema: &jsonschema.Schema{
				Not: &jsonschema.Schema{Type: "string"},
			},
			instance: 42.0,
		},
		"not fail": {
			schema: &jsonschema.Schema{
				Not: &jsonschema.Schema{Type: "string"},
			},
			instance: "hello",
			err:      "(not)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateConditional(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"if-then pass": {
			schema: &jsonschema.Schema{
				If:   &jsonschema.Schema{Type: "string"},
				Then: &jsonschema.Schema{MinLength: jsonschema.Ptr(3)},
			},
			instance: "hello",
		},
		"if-then fail": {
			schema: &jsonschema.Schema{
				If:   &jsonschema.Schema{Type: "string"},
				Then: &jsonschema.Schema{MinLength: jsonschema.Ptr(10)},
			},
			instance: "hi",
			err:      "(then)",
		},
		"if-else pass": {
			schema: &jsonschema.Schema{
				If:   &jsonschema.Schema{Type: "string"},
				Else: &jsonschema.Schema{Minimum: jsonschema.Ptr(0.0)},
			},
			instance: 5.0,
		},
		"if-else fail": {
			schema: &jsonschema.Schema{
				If:   &jsonschema.Schema{Type: "string"},
				Else: &jsonschema.Schema{Minimum: jsonschema.Ptr(0.0)},
			},
			instance: -5.0,
			err:      "(else)",
		},
		"if false no then check": {
			schema: &jsonschema.Schema{
				If:   &jsonschema.Schema{Type: "string"},
				Then: &jsonschema.Schema{MinLength: jsonschema.Ptr(100)},
			},
			instance: 42.0,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateRef(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"$ref to $defs": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Ref: "#/$defs/nameType"},
				},
				Defs: map[string]*jsonschema.Schema{
					"nameType": {Type: "string", MinLength: jsonschema.Ptr(1)},
				},
			},
			instance: map[string]any{"name": "Alice"},
		},
		"$ref fail": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Ref: "#/$defs/nameType"},
				},
				Defs: map[string]*jsonschema.Schema{
					"nameType": {Type: "string", MinLength: jsonschema.Ptr(1)},
				},
			},
			instance: map[string]any{"name": ""},
			err:      "($ref)",
		},
		"$ref to definitions (draft-07)": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Type:   "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Ref: "#/definitions/nameType"},
				},
				Definitions: map[string]*jsonschema.Schema{
					"nameType": {Type: "string"},
				},
			},
			instance: map[string]any{"name": "Alice"},
		},
		"$ref to properties": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "#/properties/name",
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
				},
			},
			instance: "hello",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		json   string
		err    string
	}{
		"valid json": {
			schema: &jsonschema.Schema{
				Type:     "object",
				Required: []string{"name"},
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
					"age":  {Type: "integer", Minimum: jsonschema.Ptr(0.0)},
				},
			},
			json: `{"name": "Alice", "age": 30}`,
		},
		"invalid json": {
			schema: &jsonschema.Schema{Type: "object"},
			json:   `{invalid`,
			err:    "JSON decode",
		},
		"json.Number preserves integers": {
			schema: &jsonschema.Schema{Type: "integer"},
			json:   `42`,
		},
		"json.Number float": {
			schema: &jsonschema.Schema{Type: "integer"},
			json:   `3.14`,
			err:    "(type)",
		},
		"trailing garbage after object": {
			schema: &jsonschema.Schema{Type: "object"},
			json:   `{"a":1} x`,
			err:    "JSON decode",
		},
		"trailing value after scalar": {
			schema: &jsonschema.Schema{Type: "boolean"},
			json:   `true false`,
			err:    "JSON decode",
		},
		"trailing number after number": {
			schema: &jsonschema.Schema{Type: "integer"},
			json:   `1 2`,
			err:    "JSON decode",
		},
		"trailing whitespace accepted": {
			schema: &jsonschema.Schema{Type: "object"},
			json:   "{\"a\":1}\n  \t\n",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.ValidateJSON(tt.schema, []byte(tt.json))
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

// TestValidateConstEnumFloatEquality covers const/enum equality between
// schema-authored float64 values (parsed without UseNumber, so 0.1 is the
// nearest float64) and instance json.Number values. The comparison expands the
// schema float through its shortest decimal so that 0.1 in the schema matches
// the literal 0.1 in the instance, while keeping JSON Schema type distinctions
// (true is not 1) and exact-representable numbers unchanged.
func TestValidateConstEnumFloatEquality(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   string
		instance string
		want     bool
	}{
		"const non-representable decimal matches": {
			schema:   `{"const":0.1}`,
			instance: `0.1`,
			want:     true,
		},
		"const non-representable decimal mismatch": {
			schema:   `{"const":0.1}`,
			instance: `0.2`,
			want:     false,
		},
		"enum non-representable decimals match second": {
			schema:   `{"enum":[0.1,0.2]}`,
			instance: `0.2`,
			want:     true,
		},
		"enum non-representable decimal not present": {
			schema:   `{"enum":[0.1,0.2]}`,
			instance: `0.3`,
			want:     false,
		},
		"const nested object decimal matches": {
			schema:   `{"const":{"x":0.1}}`,
			instance: `{"x":0.1}`,
			want:     true,
		},
		"const nested object decimal mismatch": {
			schema:   `{"const":{"x":0.1}}`,
			instance: `{"x":0.2}`,
			want:     false,
		},
		"const nested array decimal matches": {
			schema:   `{"const":[0.1,0.2]}`,
			instance: `[0.1,0.2]`,
			want:     true,
		},
		"const exact float matches": {
			schema:   `{"const":1.5}`,
			instance: `1.5`,
			want:     true,
		},
		"const integer matches decimal instance": {
			schema:   `{"const":1}`,
			instance: `1.0`,
			want:     true,
		},
		"const decimal matches integer instance": {
			schema:   `{"const":1.0}`,
			instance: `1`,
			want:     true,
		},
		"const true does not match one": {
			schema:   `{"const":true}`,
			instance: `1`,
			want:     false,
		},
		"const false does not match zero": {
			schema:   `{"const":false}`,
			instance: `0`,
			want:     false,
		},
		"enum mixed types matches decimal": {
			schema:   `{"enum":["a",0.1,true]}`,
			instance: `0.1`,
			want:     true,
		},
		"enum true does not match one": {
			schema:   `{"enum":[true,2]}`,
			instance: `1`,
			want:     false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var s jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tt.schema), &s))

			err := jsonschema.ValidateJSON(&s, []byte(tt.instance))
			if tt.want {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestValidateMultiError(t *testing.T) {
	t.Parallel()

	// Schema requiring name: string and age: integer >= 0.
	schema := &jsonschema.Schema{
		Type:     "object",
		Required: []string{"name", "age"},
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
			"age":  {Type: "integer", Minimum: jsonschema.Ptr(0.0)},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{
		"name": 123.0,
		"age":  -1.0,
	})
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	// Should have two causes (one for name type, one for age minimum).
	// The top-level error may be a container.
	errStr := err.Error()
	assert.Contains(t, errStr, "type")
	assert.Contains(t, errStr, "minimum")
}

func TestValidateErrorAs(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{Type: "string"}
	err := jsonschema.Validate(schema, 42.0)
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "type", ve.Keyword)
}

func TestValidateFormats(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format string
		value  string
		err    string
	}{
		"date-time valid": {
			format: "date-time",
			value:  "2024-01-01T12:00:00Z",
		},
		"date-time invalid": {
			format: "date-time",
			value:  "not-a-date",
			err:    "format",
		},
		"date valid": {
			format: "date",
			value:  "2024-01-01",
		},
		"date invalid": {
			format: "date",
			value:  "01/01/2024",
			err:    "format",
		},
		"email valid": {
			format: "email",
			value:  "test@example.com",
		},
		"email invalid": {
			format: "email",
			value:  "not-an-email",
			err:    "format",
		},
		"uri valid": {
			format: "uri",
			value:  "https://example.com",
		},
		"uri invalid no scheme": {
			format: "uri",
			value:  "example.com",
			err:    "format",
		},
		"ipv4 valid": {
			format: "ipv4",
			value:  "192.168.1.1",
		},
		"ipv4 invalid": {
			format: "ipv4",
			value:  "999.999.999.999",
			err:    "format",
		},
		"ipv6 valid": {
			format: "ipv6",
			value:  "::1",
		},
		"ipv6 invalid": {
			format: "ipv6",
			value:  "not-ipv6",
			err:    "format",
		},
		"uuid valid": {
			format: "uuid",
			value:  "550e8400-e29b-41d4-a716-446655440000",
		},
		"uuid invalid": {
			format: "uuid",
			value:  "not-a-uuid",
			err:    "format",
		},
		"json-pointer valid": {
			format: "json-pointer",
			value:  "/foo/bar",
		},
		"json-pointer valid empty": {
			format: "json-pointer",
			value:  "",
		},
		"json-pointer invalid": {
			format: "json-pointer",
			value:  "not/a/pointer",
			err:    "format",
		},
		"regex valid": {
			format: "regex",
			value:  "^[a-z]+$",
		},
		"regex invalid": {
			format: "regex",
			value:  "[invalid",
			err:    "format",
		},
		"hostname valid": {
			format: "hostname",
			value:  "example.com",
		},
		"hostname invalid": {
			format: "hostname",
			value:  "-invalid.com",
			err:    "format",
		},
		"uri-reference valid": {
			format: "uri-reference",
			value:  "/path/to/resource",
		},
		"time valid": {
			format: "time",
			value:  "12:00:00Z",
		},
		"time invalid": {
			format: "time",
			value:  "25:00:00",
			err:    "format",
		},
		"time single-digit hour invalid": {
			// RFC 3339 requires a two-digit hour; Go's time.Parse would otherwise
			// accept this leniently.
			format: "time",
			value:  "8:30:06Z",
			err:    "format",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{Type: "string", Format: tt.format}
			err := jsonschema.Validate(schema, tt.value, jsonschema.WithFormats(true))
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateWithFormatsDisabled(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{Type: "string", Format: "email"}
	err := jsonschema.Validate(schema, "not-an-email", jsonschema.WithFormats(false))
	require.NoError(t, err)
}

func TestValidateWithCustomFormatValidator(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{Type: "string", Format: "custom-format"}
	err := jsonschema.Validate(schema, "invalid",
		jsonschema.WithFormats(true),
		jsonschema.WithFormatValidator("custom-format", func(s string) error {
			if s != "valid" {
				return errors.New("must be 'valid'")
			}

			return nil
		}),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "custom-format")
}

func TestValidateWithContent(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		enabled  bool
		err      string
	}{
		"json media type valid": {
			schema:   &jsonschema.Schema{ContentMediaType: "application/json"},
			instance: `{"foo":"bar"}`,
			enabled:  true,
		},
		"json media type invalid": {
			schema:   &jsonschema.Schema{ContentMediaType: "application/json"},
			instance: `{:}`,
			enabled:  true,
			err:      "(contentMediaType)",
		},
		"base64 valid": {
			schema:   &jsonschema.Schema{ContentEncoding: "base64"},
			instance: "eyJmb28iOiAiYmFyIn0K",
			enabled:  true,
		},
		"base64 invalid": {
			schema:   &jsonschema.Schema{ContentEncoding: "base64"},
			instance: "eyJmb28iOi%iYmFyIn0K",
			enabled:  true,
			err:      "(contentEncoding)",
		},
		"base64 json decodes to invalid json": {
			schema:   &jsonschema.Schema{ContentEncoding: "base64", ContentMediaType: "application/json"},
			instance: "ezp9Cg==",
			enabled:  true,
			err:      "(contentMediaType)",
		},
		"non-string instance is ignored": {
			schema:   &jsonschema.Schema{ContentMediaType: "application/json"},
			instance: 100.0,
			enabled:  true,
		},
		"annotation-only by default": {
			schema:   &jsonschema.Schema{ContentMediaType: "application/json"},
			instance: `{:}`,
			enabled:  false,
		},
		"unknown encoding leaves media type unasserted": {
			// The validator cannot decode base16, so the media type cannot be
			// asserted against the decoded form; running json.Valid on the
			// still-encoded text would falsely reject hex-encoded valid JSON.
			schema:   &jsonschema.Schema{ContentEncoding: "base16", ContentMediaType: "application/json"},
			instance: "7b22666f6f223a22626172227d",
			enabled:  true,
		},
		"unknown encoding stays annotation for non-json text": {
			schema:   &jsonschema.Schema{ContentEncoding: "base16", ContentMediaType: "application/json"},
			instance: "zz-not-even-hex",
			enabled:  true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var opts []jsonschema.ValidateOption

			if tt.enabled {
				opts = append(opts, jsonschema.WithContent(true))
			}

			err := jsonschema.Validate(tt.schema, tt.instance, opts...)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateDynamicRefUnresolvableErrors(t *testing.T) {
	t.Parallel()

	// A remote $dynamicRef that no resolver can satisfy must report a
	// resolution error like the equivalent $ref, not silently accept every
	// instance.
	tests := map[string]struct {
		schema *jsonschema.Schema
		err    string
	}{
		"unresolvable remote $dynamicRef": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{
					"x": {DynamicRef: "https://example.test/no-such"},
				},
			},
			err: `cannot resolve $dynamicRef "https://example.test/no-such"`,
		},
		"unresolvable remote $ref": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{
					"x": {Ref: "https://example.test/no-such"},
				},
			},
			err: `cannot resolve $ref "https://example.test/no-such"`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, map[string]any{"x": 1.0})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}
}

func TestValidateJSONPointerFallbackBaseURI(t *testing.T) {
	t.Parallel()

	// A $ref pointing into an untyped location (an unknown keyword) is
	// materialized by the JSON-pointer fallback. A fragment-only $ref inside
	// that target must resolve against the enclosing resource's base URI
	// (https://example.test/sub), not the document root: the two resources
	// deliberately define different schemas under the same $defs name.
	root := &jsonschema.Schema{
		ID: "https://example.test/root",
		Properties: map[string]*jsonschema.Schema{
			"bar": {Ref: "#/$defs/sub/unknown-keyword"},
		},
		Defs: map[string]*jsonschema.Schema{
			"target": {Type: "integer"},
			"sub": {
				ID: "https://example.test/sub",
				Defs: map[string]*jsonschema.Schema{
					"target": {Type: "string"},
				},
				Extra: map[string]any{
					"unknown-keyword": map[string]any{"$ref": "#/$defs/target"},
				},
			},
		},
	}

	v, err := jsonschema.Compile(root)
	require.NoError(t, err)

	// The sub resource's target requires a string; resolving against the
	// document root would find the integer schema instead and invert both
	// verdicts.
	require.NoError(t, v.Validate(map[string]any{"bar": "ok"}))

	err = v.Validate(map[string]any{"bar": 7.0})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected")
}

func TestFormatAssertionVocabularyAssertsWhenRecognized(t *testing.T) {
	t.Parallel()

	// A metaschema declaring format-assertion: false still asserts format,
	// because this implementation recognizes the vocabulary; the boolean only
	// governs implementations that do not (validation §7.2.1).
	const metaID = "https://example.test/meta/format-assertion-false"

	meta := &jsonschema.Schema{
		ID: metaID,
		Vocabulary: map[string]bool{
			jsonschema.VocabCore2020:            true,
			jsonschema.VocabFormatAssertion2020: false,
		},
	}

	schema := &jsonschema.Schema{Schema: metaID, Format: "ipv4"}

	err := jsonschema.Validate(schema, "not-an-ipv4", jsonschema.WithMetaSchema(meta))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(format)")

	err = jsonschema.Validate(schema, "127.0.0.1", jsonschema.WithMetaSchema(meta))
	require.NoError(t, err)
}

func TestValidateDraft7RefIgnoresSiblings(t *testing.T) {
	t.Parallel()

	// In Draft-07, $ref siblings are ignored.
	schema := &jsonschema.Schema{
		Schema: "http://json-schema.org/draft-07/schema#",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {
				Ref:       "#/definitions/nameType",
				MinLength: jsonschema.Ptr(100), // Should be ignored in Draft-07.
			},
		},
		Definitions: map[string]*jsonschema.Schema{
			"nameType": {Type: "string"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"name": "hi"})
	require.NoError(t, err)
}

func TestValidateDraft2020RefWithSiblings(t *testing.T) {
	t.Parallel()

	// In Draft 2020-12, $ref siblings ARE processed.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {
				Ref:       "#/$defs/nameType",
				MinLength: jsonschema.Ptr(100),
			},
		},
		Defs: map[string]*jsonschema.Schema{
			"nameType": {Type: "string"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"name": "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minLength")
}

func TestValidateUnevaluatedProperties(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"unevaluatedProperties with allOf": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
				},
				AllOf: []*jsonschema.Schema{
					{
						Properties: map[string]*jsonschema.Schema{
							"age": {Type: "integer"},
						},
					},
				},
				UnevaluatedProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			},
			instance: map[string]any{"name": "Alice", "age": 30.0},
		},
		"unevaluatedProperties rejects unknown": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "string"},
				},
				UnevaluatedProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			},
			instance: map[string]any{"name": "Alice", "extra": "field"},
			err:      "value is not allowed",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateDependencies(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"draft-07 dependency strings pass": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Type:   "object",
				DependencyStrings: map[string][]string{
					"bar": {"foo"},
				},
			},
			instance: map[string]any{"bar": 1.0, "foo": 2.0},
		},
		"draft-07 dependency strings fail": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Type:   "object",
				DependencyStrings: map[string][]string{
					"bar": {"foo"},
				},
			},
			instance: map[string]any{"bar": 1.0},
			err:      "(dependencies)",
		},
		"draft-07 dependency schemas pass": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Type:   "object",
				DependencySchemas: map[string]*jsonschema.Schema{
					"bar": {
						Required: []string{"foo"},
					},
				},
			},
			instance: map[string]any{"bar": 1.0, "foo": 2.0},
		},
		"draft-07 dependency schemas fail": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Type:   "object",
				DependencySchemas: map[string]*jsonschema.Schema{
					"bar": {
						Required: []string{"foo"},
					},
				},
			},
			instance: map[string]any{"bar": 1.0},
			err:      "(required)",
		},
		"2020-12 dependent schemas pass": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependentSchemas: map[string]*jsonschema.Schema{
					"bar": {
						Required: []string{"foo"},
					},
				},
			},
			instance: map[string]any{"bar": 1.0, "foo": 2.0},
		},
		"2020-12 dependent schemas fail": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependentSchemas: map[string]*jsonschema.Schema{
					"bar": {
						Required: []string{"foo"},
					},
				},
			},
			instance: map[string]any{"bar": 1.0},
			err:      "(required)",
		},
		// The legacy `dependencies` keyword is honored under 2020-12 for
		// backward compatibility, not silently ignored.
		"2020-12 legacy dependency strings pass": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependencyStrings: map[string][]string{
					"bar": {"foo"},
				},
			},
			instance: map[string]any{"bar": 1.0, "foo": 2.0},
		},
		"2020-12 legacy dependency strings fail": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependencyStrings: map[string][]string{
					"bar": {"foo"},
				},
			},
			instance: map[string]any{"bar": 1.0},
			err:      "(dependencies)",
		},
		"2020-12 legacy dependency schemas fail": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependencySchemas: map[string]*jsonschema.Schema{
					"bar": {
						Required: []string{"foo"},
					},
				},
			},
			instance: map[string]any{"bar": 1.0},
			err:      "(required)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateCircularRef(t *testing.T) {
	t.Parallel()

	// A recursive schema: a tree node with optional children.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Ref:    "#/$defs/node",
		Defs: map[string]*jsonschema.Schema{
			"node": {
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"value": {Type: "string"},
					"children": {
						Type:  "array",
						Items: &jsonschema.Schema{Ref: "#/$defs/node"},
					},
				},
			},
		},
	}

	instance := map[string]any{
		"value": "root",
		"children": []any{
			map[string]any{
				"value":    "child1",
				"children": []any{},
			},
		},
	}

	err := jsonschema.Validate(schema, instance)
	require.NoError(t, err)
}

func TestValidateUnevaluatedItems(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"unevaluatedItems with prefixItems": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "array",
				PrefixItems: []*jsonschema.Schema{
					{Type: "string"},
				},
				UnevaluatedItems: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			},
			instance: []any{"hello"},
		},
		"unevaluatedItems rejects extra items": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "array",
				PrefixItems: []*jsonschema.Schema{
					{Type: "string"},
				},
				UnevaluatedItems: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			},
			instance: []any{"hello", 42.0},
			err:      "value is not allowed",
		},
		"unevaluatedItems with allOf contributing items": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "array",
				PrefixItems: []*jsonschema.Schema{
					{Type: "string"},
				},
				AllOf: []*jsonschema.Schema{
					{
						// Empty items schema accepts all — sets allItems annotation.
						Items: &jsonschema.Schema{},
					},
				},
				UnevaluatedItems: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			},
			instance: []any{"hello", 42.0},
		},
		"unevaluatedItems with contains": {
			schema: &jsonschema.Schema{
				Schema:           "https://json-schema.org/draft/2020-12/schema",
				Type:             "array",
				Contains:         &jsonschema.Schema{Type: "string"},
				UnevaluatedItems: &jsonschema.Schema{Type: "integer"},
			},
			instance: []any{"hello", 42.0},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateMaxContains(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"maxContains pass": {
			schema: &jsonschema.Schema{
				Type:        "array",
				Contains:    &jsonschema.Schema{Type: "string"},
				MaxContains: jsonschema.Ptr(2),
			},
			instance: []any{"a", "b", 1.0},
		},
		"maxContains fail": {
			schema: &jsonschema.Schema{
				Type:        "array",
				Contains:    &jsonschema.Schema{Type: "string"},
				MaxContains: jsonschema.Ptr(1),
			},
			instance: []any{"a", "b", 1.0},
			err:      "(maxContains)",
		},
		"minContains and maxContains together": {
			schema: &jsonschema.Schema{
				Type:        "array",
				Contains:    &jsonschema.Schema{Type: "string"},
				MinContains: jsonschema.Ptr(1),
				MaxContains: jsonschema.Ptr(2),
			},
			instance: []any{"a", 1.0, 2.0},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateDraft7AdditionalItems(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"items array with additionalItems pass": {
			schema: &jsonschema.Schema{
				Schema:     "http://json-schema.org/draft-07/schema#",
				Type:       "array",
				ItemsArray: []*jsonschema.Schema{{Type: "string"}, {Type: "integer"}},
				AdditionalItems: &jsonschema.Schema{
					Type: "boolean",
				},
			},
			instance: []any{"hello", 42.0, true},
		},
		"items array validates prefix": {
			schema: &jsonschema.Schema{
				Schema:     "http://json-schema.org/draft-07/schema#",
				Type:       "array",
				ItemsArray: []*jsonschema.Schema{{Type: "string"}, {Type: "integer"}},
			},
			instance: []any{42.0, "wrong"},
			err:      "(type)",
		},
		"additionalItems rejects wrong type": {
			schema: &jsonschema.Schema{
				Schema:          "http://json-schema.org/draft-07/schema#",
				Type:            "array",
				ItemsArray:      []*jsonschema.Schema{{Type: "string"}},
				AdditionalItems: &jsonschema.Schema{Type: "integer"},
			},
			instance: []any{"hello", "not-integer"},
			err:      "(type)",
		},
		"additionalItems false rejects extra": {
			schema: &jsonschema.Schema{
				Schema:          "http://json-schema.org/draft-07/schema#",
				Type:            "array",
				ItemsArray:      []*jsonschema.Schema{{Type: "string"}},
				AdditionalItems: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			},
			instance: []any{"hello", 42.0},
			err:      "value is not allowed",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestValidateRefToRoot(t *testing.T) {
	t.Parallel()

	// A $ref to "#" within the root's own properties creates a recursive
	// reference. The validator distinguishes cycles by (schema, instancePath)
	// so different instance paths are validated correctly.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
			"child": {
				Ref: "#",
			},
		},
	}

	// Valid: nested child matches root schema.
	err := jsonschema.Validate(schema, map[string]any{
		"name": "parent",
		"child": map[string]any{
			"name": "child",
		},
	})
	require.NoError(t, err)

	// Invalid: child.name is not a string — the recursive $ref correctly
	// validates the nested object against the root schema.
	err = jsonschema.Validate(schema, map[string]any{
		"name": "parent",
		"child": map[string]any{
			"name": 123.0,
		},
	})
	require.Error(t, err)
}

func TestValidateRefCausesNesting(t *testing.T) {
	t.Parallel()

	// Verify $ref wraps errors in a Causes entry with Keyword: "$ref".
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Ref:    "#/$defs/name",
		Defs: map[string]*jsonschema.Schema{
			"name": {Type: "string", MinLength: jsonschema.Ptr(3)},
		},
	}

	err := jsonschema.Validate(schema, "hi")
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "$ref", ve.Keyword)
	require.NotEmpty(t, ve.Causes)
	assert.Equal(t, "minLength", ve.Causes[0].Keyword)
}

func TestValidateInstancePaths(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"address": {
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"city": {Type: "string", MinLength: jsonschema.Ptr(1)},
				},
			},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{
		"address": map[string]any{
			"city": "",
		},
	})
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "/address/city")
}

func TestValidateVocabularyResolution(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		opts     []jsonschema.ValidateOption
		err      string
	}{
		"default all vocabs active": {
			schema:   &jsonschema.Schema{Schema: "https://json-schema.org/draft/2020-12/schema", Type: "string"},
			instance: 42.0,
			err:      "(type)",
		},
		"draft7 ignores vocab settings": {
			// WithVocabularies disabling validation has no effect on Draft-07.
			schema:   &jsonschema.Schema{Schema: "http://json-schema.org/draft-07/schema#", Type: "string"},
			instance: 42.0,
			opts: []jsonschema.ValidateOption{
				jsonschema.WithVocabularies(map[string]bool{
					jsonschema.VocabCore2020: true,
				}),
			},
			err: "(type)",
		},
		"WithVocabularies disables validation vocab": {
			schema: &jsonschema.Schema{
				Schema:    "https://json-schema.org/draft/2020-12/schema",
				Type:      "string",
				MinLength: jsonschema.Ptr(10),
			},
			instance: "hi",
			opts: []jsonschema.ValidateOption{
				jsonschema.WithVocabularies(map[string]bool{
					jsonschema.VocabCore2020:       true,
					jsonschema.VocabApplicator2020: true,
				}),
			},
		},
		"WithMetaSchema lookup": {
			schema: &jsonschema.Schema{
				Schema:   "https://example.com/my-meta",
				Type:     "string",
				Required: []string{"foo"},
			},
			instance: map[string]any{},
			opts: []jsonschema.ValidateOption{
				jsonschema.WithMetaSchema(&jsonschema.Schema{
					ID: "https://example.com/my-meta",
					Vocabulary: map[string]bool{
						jsonschema.VocabCore2020:       true,
						jsonschema.VocabApplicator2020: true,
						// Validation vocab absent, so disabled.
					},
				}),
			},
			// Type and required are validation vocab, so both skipped.
		},
		"WithVocabularies overrides WithMetaSchema": {
			schema: &jsonschema.Schema{
				Schema: "https://example.com/my-meta",
				Type:   "string",
			},
			instance: 42.0,
			opts: []jsonschema.ValidateOption{
				jsonschema.WithMetaSchema(&jsonschema.Schema{
					ID: "https://example.com/my-meta",
					Vocabulary: map[string]bool{
						jsonschema.VocabCore2020:       true,
						jsonschema.VocabApplicator2020: true,
						// Validation disabled in metaschema.
					},
				}),
				// Override re-enables validation.
				jsonschema.WithVocabularies(map[string]bool{
					jsonschema.VocabCore2020:       true,
					jsonschema.VocabApplicator2020: true,
					jsonschema.VocabValidation2020: true,
				}),
			},
			err: "(type)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance, tt.opts...)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestWithMetaSchemaNil(t *testing.T) {
	t.Parallel()

	// A nil metaschema is a no-op: it must not panic and must leave validation
	// behaving exactly as if the option were absent.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
	}

	var withNil, without error

	require.NotPanics(t, func() {
		withNil = jsonschema.Validate(schema, 42.0, jsonschema.WithMetaSchema(nil))
	}, "WithMetaSchema(nil) must not panic")

	without = jsonschema.Validate(schema, 42.0)

	require.Error(t, withNil, "the type constraint still applies with a nil metaschema")
	assert.Contains(t, withNil.Error(), "(type)")

	if without == nil {
		assert.NoError(t, withNil)
	} else {
		assert.Equal(t, without.Error(), withNil.Error(),
			"WithMetaSchema(nil) must match validation with the option absent")
	}
}

func TestValidateVocabularyValidationDisabled(t *testing.T) {
	t.Parallel()

	noValidation := []jsonschema.ValidateOption{
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:        true,
			jsonschema.VocabApplicator2020:  true,
			jsonschema.VocabUnevaluated2020: true,
		}),
	}

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
	}{
		"type skipped": {
			schema:   &jsonschema.Schema{Schema: "https://json-schema.org/draft/2020-12/schema", Type: "string"},
			instance: 42.0,
		},
		"enum skipped": {
			schema:   &jsonschema.Schema{Schema: "https://json-schema.org/draft/2020-12/schema", Enum: []any{"a", "b"}},
			instance: "c",
		},
		"const skipped": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Const:  jsonschema.Ptr(any("hello")),
			},
			instance: "world",
		},
		"minimum skipped": {
			schema: &jsonschema.Schema{
				Schema:  "https://json-schema.org/draft/2020-12/schema",
				Minimum: jsonschema.Ptr(10.0),
			},
			instance: 1.0,
		},
		"required skipped": {
			schema: &jsonschema.Schema{
				Schema:   "https://json-schema.org/draft/2020-12/schema",
				Required: []string{"name"},
			},
			instance: map[string]any{},
		},
		"minLength skipped": {
			schema: &jsonschema.Schema{
				Schema:    "https://json-schema.org/draft/2020-12/schema",
				MinLength: jsonschema.Ptr(10),
			},
			instance: "hi",
		},
		"minItems skipped": {
			schema: &jsonschema.Schema{
				Schema:   "https://json-schema.org/draft/2020-12/schema",
				MinItems: jsonschema.Ptr(5),
			},
			instance: []any{1.0},
		},
		"uniqueItems skipped": {
			schema:   &jsonschema.Schema{Schema: "https://json-schema.org/draft/2020-12/schema", UniqueItems: true},
			instance: []any{1.0, 1.0},
		},
		"minProperties skipped": {
			schema: &jsonschema.Schema{
				Schema:        "https://json-schema.org/draft/2020-12/schema",
				MinProperties: jsonschema.Ptr(5),
			},
			instance: map[string]any{},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance, noValidation...)
			require.NoError(t, err)
		})
	}
}

func TestValidateVocabularyApplicatorDisabled(t *testing.T) {
	t.Parallel()

	noApplicator := []jsonschema.ValidateOption{
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:       true,
			jsonschema.VocabValidation2020: true,
		}),
	}

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
	}{
		"properties skipped": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Properties: map[string]*jsonschema.Schema{
					"name": {Type: "integer"},
				},
			},
			instance: map[string]any{"name": "not-integer"},
		},
		"allOf skipped": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				AllOf: []*jsonschema.Schema{
					{Type: "string"},
					{Type: "integer"},
				},
			},
			instance: true,
		},
		"if-then-else skipped": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				If:     &jsonschema.Schema{Type: "string"},
				Then:   &jsonschema.Schema{MinLength: jsonschema.Ptr(100)},
			},
			instance: "short",
		},
		"contains skipped": {
			schema: &jsonschema.Schema{
				Schema:   "https://json-schema.org/draft/2020-12/schema",
				Contains: &jsonschema.Schema{Type: "string"},
			},
			instance: []any{1.0, 2.0},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance, noApplicator...)
			require.NoError(t, err)
		})
	}
}

func TestValidateVocabularyUnevaluatedDisabled(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
		},
		UnevaluatedProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}

	// With unevaluated vocab disabled, extra properties are not flagged.
	err := jsonschema.Validate(schema, map[string]any{"name": "Alice", "extra": "field"},
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:       true,
			jsonschema.VocabApplicator2020: true,
			jsonschema.VocabValidation2020: true,
		}),
	)
	require.NoError(t, err)
}

func TestValidateVocabularyUnknownRequired(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{Schema: "https://json-schema.org/draft/2020-12/schema"}
	err := jsonschema.Validate(schema, "anything",
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:                           true,
			"https://example.com/vocab/unknown-required-vocab": true,
		}),
	)
	require.Error(t, err)
	require.ErrorIs(t, err, jsonschema.ErrUnknownVocabulary)
}

func TestValidateVocabularyUnknownOptional(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
	}
	// Unknown optional vocabulary (false) is silently ignored.
	err := jsonschema.Validate(schema, "hello",
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:                           true,
			jsonschema.VocabValidation2020:                     true,
			"https://example.com/vocab/unknown-optional-vocab": false,
		}),
	)
	require.NoError(t, err)
}

func TestValidateVocabularyMetaSchemaIntegration(t *testing.T) {
	t.Parallel()

	// Metaschema that disables validation vocab — type/required/minimum are skipped.
	metaSchema := &jsonschema.Schema{
		ID: "https://example.com/meta/no-validation",
		Vocabulary: map[string]bool{
			jsonschema.VocabCore2020:       true,
			jsonschema.VocabApplicator2020: true,
		},
	}

	schema := &jsonschema.Schema{
		Schema:   "https://example.com/meta/no-validation",
		Type:     "object",
		Required: []string{"name"},
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string", MinLength: jsonschema.Ptr(10)},
		},
	}

	// Validation keywords (type, required, minLength) are skipped because
	// the metaschema only declares core + applicator.
	// Properties still apply (applicator vocab), but the type constraint
	// inside the property schema is also skipped.
	err := jsonschema.Validate(schema, map[string]any{"name": "hi"},
		jsonschema.WithMetaSchema(metaSchema),
	)
	require.NoError(t, err)

	// Missing property entirely is also fine (required is validation vocab).
	err = jsonschema.Validate(schema, map[string]any{},
		jsonschema.WithMetaSchema(metaSchema),
	)
	require.NoError(t, err)
}

func TestValidateVocabularyApplicatorActiveValidationDisabled(t *testing.T) {
	t.Parallel()

	// Replicates vocabulary.json pattern: applicator still works while
	// validation is disabled.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"}, // type is validation → skipped
			"age":  {Type: "integer"},
		},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}

	opts := []jsonschema.ValidateOption{
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:       true,
			jsonschema.VocabApplicator2020: true,
		}),
	}

	// Known properties pass — type constraints inside property schemas are
	// validation vocab and therefore skipped, but properties itself (applicator)
	// still tracks which properties were evaluated.
	err := jsonschema.Validate(schema, map[string]any{"name": 42.0, "age": "not-int"}, opts...)
	require.NoError(t, err)

	// Unknown property is still rejected by additionalProperties (applicator).
	err = jsonschema.Validate(schema, map[string]any{"name": "Alice", "unknown": true}, opts...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "value is not allowed")
}

// mapResolver is a test helper that resolves URIs from a static map.
type mapResolver map[string]*jsonschema.Schema

func (m mapResolver) ResolveRef(uri string) (*jsonschema.Schema, error) {
	if s, ok := m[uri]; ok {
		return s, nil
	}

	//nolint:nilnil // A miss returns (nil, nil): the validator treats it as "unresolved, skip".
	return nil, nil
}

func TestValidateWithRefResolver(t *testing.T) {
	t.Parallel()

	integerSchema := &jsonschema.Schema{Type: "integer"}

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		resolver jsonschema.RefResolver
		err      string
	}{
		"remote ref resolves successfully": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "http://example.com/integer.json",
			},
			instance: 42.0,
			resolver: mapResolver{
				"http://example.com/integer.json": integerSchema,
			},
		},
		"remote ref rejects invalid data": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "http://example.com/integer.json",
			},
			instance: "not an integer",
			resolver: mapResolver{
				"http://example.com/integer.json": integerSchema,
			},
			err: "($ref)",
		},
		"remote ref with JSON Pointer fragment": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "http://example.com/schemas.json#/$defs/name",
			},
			instance: "hello",
			resolver: mapResolver{
				"http://example.com/schemas.json": {
					Defs: map[string]*jsonschema.Schema{
						"name": {Type: "string", MinLength: jsonschema.Ptr(1)},
					},
				},
			},
		},
		"remote ref with anchor": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "http://example.com/anchored.json#myAnchor",
			},
			instance: 42.0,
			resolver: mapResolver{
				"http://example.com/anchored.json": {
					ID: "http://example.com/anchored.json",
					Defs: map[string]*jsonschema.Schema{
						"int": {Anchor: "myAnchor", Type: "integer"},
					},
				},
			},
		},
		"transitive remote refs": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "http://example.com/a.json",
			},
			instance: 42.0,
			resolver: mapResolver{
				"http://example.com/a.json": {
					ID:  "http://example.com/a.json",
					Ref: "http://example.com/b.json",
				},
				"http://example.com/b.json": {
					ID:  "http://example.com/b.json",
					Ref: "http://example.com/c.json",
				},
				"http://example.com/c.json": integerSchema,
			},
		},
		"resolver error produces validation error": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "http://example.com/missing.json",
			},
			instance: 42.0,
			resolver: errResolver{},
			err:      "ref resolve",
		},
		"resolver returns nil produces error": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "http://example.com/unknown.json",
			},
			instance: 42.0,
			resolver: mapResolver{},
			err:      "cannot resolve",
		},
		"no resolver produces error": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Ref:    "http://example.com/integer.json",
			},
			instance: "anything",
			err:      "cannot resolve",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var opts []jsonschema.ValidateOption

			if tt.resolver != nil {
				opts = append(opts, jsonschema.WithRefResolver(tt.resolver))
			}

			err := jsonschema.Validate(tt.schema, tt.instance, opts...)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

// errResolver always returns an error.
type errResolver struct{}

func (errResolver) ResolveRef(string) (*jsonschema.Schema, error) {
	return nil, errors.New("connection refused")
}

func TestValidateRefResolverCaching(t *testing.T) {
	t.Parallel()

	integerSchema := &jsonschema.Schema{Type: "integer"}

	var callCount atomic.Int64

	resolver := &countingResolver{
		schemas:   map[string]*jsonschema.Schema{"http://example.com/int.json": integerSchema},
		callCount: &callCount,
	}

	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"a": {Ref: "http://example.com/int.json"},
			"b": {Ref: "http://example.com/int.json"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"a": 1.0, "b": 2.0},
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), callCount.Load(),
		"resolver should be called once per URI, got %d", callCount.Load())
}

// countingResolver tracks how many times ResolveRef is called. The counter is
// an atomic so the test stays race-free even if ref resolution is parallelized.
type countingResolver struct {
	schemas   map[string]*jsonschema.Schema
	callCount *atomic.Int64
}

func (r *countingResolver) ResolveRef(uri string) (*jsonschema.Schema, error) {
	r.callCount.Add(1)

	if s, ok := r.schemas[uri]; ok {
		return s, nil
	}

	//nolint:nilnil // A miss returns (nil, nil): the validator treats it as "unresolved, skip".
	return nil, nil
}

func TestFormatAnnotationVocabSuppressesErrors(t *testing.T) {
	t.Parallel()

	// When only format-annotation vocabulary is active (not format-assertion),
	// format keywords should produce annotations only, not validation errors.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
		Format: "email",
	}

	// With only format-annotation active, invalid formats should NOT produce errors.
	err := jsonschema.Validate(schema, "not-an-email",
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:             true,
			jsonschema.VocabApplicator2020:       true,
			jsonschema.VocabValidation2020:       true,
			jsonschema.VocabFormatAnnotation2020: true,
			// The format-assertion vocabulary is NOT active.
		}),
	)
	// Per spec, format-annotation means collect as annotation, not validate.
	require.NoError(t, err, "format-annotation should not produce validation errors")
}

func TestInstanceTypeRecognizesLargeFloat64Integer(t *testing.T) {
	t.Parallel()

	// A float64 integer beyond the int64 range is still recognized as an
	// integer: instanceType uses math.Trunc rather than an int64 cast that
	// would wrap around.
	schema := &jsonschema.Schema{
		Type: "integer",
	}

	largeFloat := float64(math.MaxInt64) + 1024 // Exceeds int64, but is an integer
	err := jsonschema.Validate(schema, largeFloat)
	require.NoError(t, err, "large float64 integers should be recognized as integers")
}

func TestInstanceMatchesTypeRecognizesLargeFloat64Integer(t *testing.T) {
	t.Parallel()

	// InstanceMatchesType recognizes the same out-of-int64-range float64 integer
	// when "integer" appears in a multi-type list.
	schema := &jsonschema.Schema{
		Types: []string{"integer", "string"},
	}

	largeFloat := float64(math.MaxInt64) + 1024
	err := jsonschema.Validate(schema, largeFloat)
	require.NoError(t, err, "large float64 integers should match 'integer' type")
}

func TestHashValueDistinguishesLargeFloat64Integers(t *testing.T) {
	t.Parallel()

	// Distinct float64 integers beyond the int64 range hash distinctly: the fast
	// int64 path is bounded, and larger values fall back to exact big.Rat keys.
	schema := &jsonschema.Schema{
		Type:        "array",
		UniqueItems: true,
	}

	a := float64(math.MaxInt64) + 1024
	b := float64(math.MaxInt64) + 2048
	err := jsonschema.Validate(schema, []any{a, b})
	require.NoError(t, err, "distinct large float64 integers should be treated as unique")
}

func TestHashValueDistinguishesLargeJSONNumbers(t *testing.T) {
	t.Parallel()

	// Distinct json.Number integers beyond the int64 range hash distinctly: the
	// int64 fast path is bounded and larger values use exact big.Rat keys.
	schema := &jsonschema.Schema{
		Type:        "array",
		UniqueItems: true,
	}

	data := `[9999999999999999999, 9999999999999999998]`
	err := jsonschema.ValidateJSON(schema, []byte(data))
	require.NoError(t, err, "distinct large json.Number integers should be treated as unique")
}

func TestContentSchemaIsAnnotationOnly(t *testing.T) {
	t.Parallel()

	// Per 2020-12 spec 8.5, contentSchema is an annotation-only keyword: it
	// describes decoded content, which this package never decodes, so it never
	// affects validity. A schema carrying only contentSchema therefore accepts
	// any instance, including ones the contentSchema itself would reject.
	schema := &jsonschema.Schema{
		ContentSchema: &jsonschema.Schema{Type: "string"},
	}

	require.NoError(t, jsonschema.Validate(schema, 42.0),
		"contentSchema must not be asserted against the raw instance")
	require.NoError(t, jsonschema.Validate(schema, "anything"),
		"contentSchema is an annotation even for string instances")
}

func TestUnresolvableRefIsError(t *testing.T) {
	t.Parallel()

	// An unresolvable local $ref (typo'd $defs key) is a validation error.
	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"name": {Ref: "#/$defs/Usre"}, // Typo: should be "User"
		},
		Defs: map[string]*jsonschema.Schema{
			"User": {Type: "string"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"name": 42})
	require.Error(t, err, "unresolvable $ref should produce a validation error")
}

func TestRefArrayIndexRFC6901Canonical(t *testing.T) {
	t.Parallel()

	// RFC 6901 array-index reference tokens admit only "0" or a nonzero leading
	// digit followed by digits. A $ref into an allOf index resolves for the
	// canonical "1" and never resolves to that subschema for the non-canonical
	// "01" or "+1".
	//
	// The allOf members assert nothing on their own (the constraint lives in a
	// $defs nested under index 1), so the only failure source is the ref. The
	// canonical form reaches a const the instance violates. A leading-zero index
	// is rejected outright by RFC 6901 resolution and surfaces as a ref error; a
	// signed index resolves to nothing and leaves the instance unconstrained.
	newSchema := func(ref string) *jsonschema.Schema {
		return &jsonschema.Schema{
			Ref: ref,
			AllOf: []*jsonschema.Schema{
				{},
				{Defs: map[string]*jsonschema.Schema{
					"strict": {Const: jsonschema.Ptr[any]("only-this")},
				}},
			},
		}
	}

	tests := map[string]struct {
		ref     string
		wantErr string // substring required in the error; "" means no error
	}{
		"canonical index resolves and asserts the const": {
			ref:     "#/allOf/1/$defs/strict",
			wantErr: "(const)",
		},
		"leading-zero index is a ref resolution error": {
			ref:     "#/allOf/01/$defs/strict",
			wantErr: "01",
		},
		"signed index leaves the instance unconstrained": {
			ref:     "#/allOf/+1/$defs/strict",
			wantErr: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(newSchema(tt.ref), "not-only-this")
			if tt.wantErr == "" {
				require.NoError(t, err,
					"non-canonical array index %q must not resolve to the const subschema", tt.ref)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSchemaAtJSONPointerArrayIndexCanonical(t *testing.T) {
	t.Parallel()

	// The JSON-encoding fallback walks array values held in unknown keywords,
	// which typed traversal cannot reach. It too honors RFC 6901 canonical array
	// indices: a $ref into an Extra slice resolves for "1", while "01" is
	// rejected by RFC 6901 resolution and surfaces as a ref error.
	newSchema := func(ref string) *jsonschema.Schema {
		return &jsonschema.Schema{
			Ref: ref,
			Extra: map[string]any{
				"x-cases": []any{
					map[string]any{},
					map[string]any{"const": "only-this"},
				},
			},
		}
	}

	tests := map[string]struct {
		ref     string
		wantErr string // substring required in the error; "" means no error
	}{
		"canonical index resolves through the JSON fallback": {
			ref:     "#/x-cases/1",
			wantErr: "(const)",
		},
		"leading-zero index is a ref resolution error": {
			ref:     "#/x-cases/01",
			wantErr: "01",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(newSchema(tt.ref), "not-only-this")
			if tt.wantErr == "" {
				require.NoError(t, err,
					"non-canonical array index %q must not resolve through the JSON fallback", tt.ref)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInvalidPatternFailsClosed(t *testing.T) {
	t.Parallel()

	// A pattern Go's RE2 cannot compile fails closed: no string matches it.
	schema := &jsonschema.Schema{
		Type:    "string",
		Pattern: "[invalid",
	}

	err := jsonschema.Validate(schema, "anything")
	require.Error(t, err, "invalid pattern regex should produce a validation error")
}

func TestAllOfAnnotationMergeOnPartialFailure(t *testing.T) {
	t.Parallel()

	// When allOf has subschema 0 pass and subschema 1 fail, subschema 0's
	// annotations should NOT be merged because allOf as a whole failed.
	schema := &jsonschema.Schema{
		Type: "object",
		AllOf: []*jsonschema.Schema{
			{
				Properties: map[string]*jsonschema.Schema{
					"a": {Type: "string"},
				},
			},
			{
				Properties: map[string]*jsonschema.Schema{
					"b": {Type: "integer"},
				},
				Required: []string{"b"},
			},
		},
		UnevaluatedProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, // false
	}

	// Instance: "a" is present and valid for allOf[0], but "b" is missing (allOf[1] fails).
	// Since allOf fails, "a" should NOT be considered evaluated.
	// Then unevaluatedProperties should reject "a".
	instance := map[string]any{"a": "hello"}
	err := jsonschema.Validate(schema, instance)
	require.Error(t, err)

	// The error should mention unevaluatedProperties for "a" since allOf failed
	// and its annotations should have been rolled back.
	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	found := findErrorByKeyword(ve, "unevaluatedProperties")
	assert.True(t, found,
		"allOf failure should roll back annotations; unevaluatedProperties should reject 'a', got: %s", err)
}

func TestIfAnnotationsMergedBeforeThen(t *testing.T) {
	t.Parallel()

	// When if passes but then fails, if's annotations should not be merged.
	schema := &jsonschema.Schema{
		Type: "object",
		If: &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{
				"type": {Const: jsonschema.Ptr[any]("a")},
			},
		},
		Then: &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{
				"value": {Type: "integer"},
			},
			Required: []string{"value"},
		},
		UnevaluatedProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, // false
	}

	// Instance: if passes (type=="a"), but then fails (missing "value").
	// Properties matched by if should NOT be marked evaluated.
	instance := map[string]any{"type": "a"}
	err := jsonschema.Validate(schema, instance)
	require.Error(t, err, "if+then failure should not mark if-properties as evaluated")
}

func TestItemsAfterPrefixItemsAnnotationOnFailure(t *testing.T) {
	t.Parallel()

	// The items annotation should only be set when validation succeeds.
	schema := &jsonschema.Schema{
		Type: "array",
		PrefixItems: []*jsonschema.Schema{
			{Type: "integer"},
		},
		Items:            &jsonschema.Schema{Type: "string"},
		UnevaluatedItems: &jsonschema.Schema{Not: &jsonschema.Schema{}}, // false
	}

	// Array with prefix item valid, but remaining item failing items validation.
	// The items keyword should not annotate allItems=true since validation failed.
	instance := []any{1.0, 42.0} // 42.0 is not a string
	err := jsonschema.Validate(schema, instance)
	require.Error(t, err, "items failure should not suppress unevaluatedItems")
}

func TestPrefixItemsAnnotationGatedOnSuccess(t *testing.T) {
	t.Parallel()

	// PrefixItems annotates every index it applied a subschema to, even when the
	// item fails validation, so unevaluatedItems must not re-validate it. With
	// unevaluatedItems:false the index draws a second, spurious error if the
	// annotation is (wrongly) gated on per-item success — so this asserts its
	// absence rather than relying on a permissive unevaluatedItems subschema.
	schema := &jsonschema.Schema{
		Type: "array",
		PrefixItems: []*jsonschema.Schema{
			{Type: "string"}, // fails for an integer
		},
		UnevaluatedItems: &jsonschema.Schema{Not: &jsonschema.Schema{}}, // false
	}

	// First item fails prefixItems but is still evaluated by it.
	instance := []any{42.0}
	err := jsonschema.Validate(schema, instance)
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	assert.False(t, findErrorByKeyword(ve, "unevaluatedItems"),
		"item evaluated by prefixItems must not be re-flagged by unevaluatedItems, got: %s", err)
	assert.True(t, findErrorByKeyword(ve, "type"),
		"the failing prefix item should still produce its type error, got: %s", err)
}

func TestContainsAnnotationsLeakOnFailure(t *testing.T) {
	t.Parallel()

	// When contains matches some items but ultimately fails (matchCount < minContains),
	// annotations should NOT be recorded.
	schema := &jsonschema.Schema{
		Type:             "array",
		Contains:         &jsonschema.Schema{Type: "string"},
		MinContains:      jsonschema.Ptr(3),                             // Requires 3 string items
		UnevaluatedItems: &jsonschema.Schema{Not: &jsonschema.Schema{}}, // false
	}

	// Only 1 string item: contains matches it but overall fails (need 3).
	// The string item should NOT be marked as evaluated.
	instance := []any{"hello", 1.0, 2.0}
	err := jsonschema.Validate(schema, instance)
	require.Error(t, err)

	// The unevaluatedItems keyword should catch all items since contains failed.
	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	found := findErrorByKeyword(ve, "unevaluatedItems")
	assert.True(t, found,
		"contains failure should not leak annotations; unevaluatedItems should catch all items, got: %s", err)
}

func TestMinMaxContainsDraftGated(t *testing.T) {
	t.Parallel()

	// MinContains/maxContains are 2019-09/2020-12 keywords. Under Draft-07 they
	// are unknown and must be ignored, so contains keeps its bare "at least one
	// match" meaning. Under 2020-12 they apply.
	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance []any
		wantErr  bool
	}{
		"draft-07 ignores minContains": {
			schema: &jsonschema.Schema{
				Schema:      "http://json-schema.org/draft-07/schema#",
				Type:        "array",
				Contains:    &jsonschema.Schema{Const: jsonschema.Ptr[any](1.0)},
				MinContains: jsonschema.Ptr(2),
			},
			instance: []any{1.0},
			wantErr:  false,
		},
		"draft-07 ignores maxContains": {
			schema: &jsonschema.Schema{
				Schema:      "http://json-schema.org/draft-07/schema#",
				Type:        "array",
				Contains:    &jsonschema.Schema{Const: jsonschema.Ptr[any](1.0)},
				MaxContains: jsonschema.Ptr(1),
			},
			instance: []any{1.0, 1.0},
			wantErr:  false,
		},
		"2020-12 enforces minContains": {
			schema: &jsonschema.Schema{
				Schema:      "https://json-schema.org/draft/2020-12/schema",
				Type:        "array",
				Contains:    &jsonschema.Schema{Const: jsonschema.Ptr[any](1.0)},
				MinContains: jsonschema.Ptr(2),
			},
			instance: []any{1.0},
			wantErr:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tc.schema, tc.instance)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRefPercentEncodedPointer(t *testing.T) {
	t.Parallel()

	// A $ref whose JSON Pointer targets a property whose name contains '%' must
	// resolve to that property's schema. Url.Parse percent-decodes the fragment
	// once; decoding a second time corrupts the name, drops the target, and the
	// unresolved local ref is silently skipped — under-validating the instance.
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"a%41b": {Type: "integer"},
			"x":     {Ref: "#/properties/a%2541b"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"x": "not-an-integer"})
	require.Error(t, err, "the $ref must resolve to the integer schema and reject the string")
}

func TestValidateNonFiniteFloat(t *testing.T) {
	t.Parallel()

	// JSON cannot represent Inf/NaN, but a Go float64 instance can carry them.
	// Numeric keywords and uniqueItems must skip such values without panicking,
	// preserving the documented concurrency-safe Validate guarantee.
	nonFinite := map[string]float64{
		"+Inf": math.Inf(1),
		"-Inf": math.Inf(-1),
		"NaN":  math.NaN(),
	}

	numeric := map[string]*jsonschema.Schema{
		"minimum":          {Minimum: jsonschema.Ptr(0.0)},
		"maximum":          {Maximum: jsonschema.Ptr(10.0)},
		"exclusiveMinimum": {ExclusiveMinimum: jsonschema.Ptr(0.0)},
		"exclusiveMaximum": {ExclusiveMaximum: jsonschema.Ptr(10.0)},
		"multipleOf":       {MultipleOf: jsonschema.Ptr(2.0)},
	}

	for kw, sub := range numeric {
		t.Run("numeric/"+kw, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"x": sub},
			}

			for label, f := range nonFinite {
				err := jsonschema.Validate(schema, map[string]any{"x": f})
				assert.NoError(t, err, "%s should skip numeric keyword %s, not fail or panic", label, kw)
			}
		})
	}

	t.Run("uniqueItems", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{Type: "array", UniqueItems: true}

		for label, f := range nonFinite {
			err := jsonschema.Validate(schema, []any{f, 1.0})
			assert.NoError(t, err, "%s must hash without panicking", label)
		}
	})
}

func TestAdditionalPropertiesAnnotationUnconditional(t *testing.T) {
	t.Parallel()

	// The additionalProperties keyword should annotate only the properties it processed,
	// not set a blanket allProperties=true.
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"a": {Type: "string"},
		},
		AdditionalProperties:  &jsonschema.Schema{Type: "integer"},
		UnevaluatedProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, // false
	}

	// "b" is an additional property validated by additionalProperties.
	// "a" is validated by properties.
	// The unevaluatedProperties keyword should see that all are covered.
	instance := map[string]any{"a": "hello", "b": 42.0}
	err := jsonschema.Validate(schema, instance)
	require.NoError(t, err)
}

func TestDetectDraftMissingTrailingHash(t *testing.T) {
	t.Parallel()

	// Draft7 URI without trailing # should still be detected as Draft7.
	schema := &jsonschema.Schema{
		Schema: "http://json-schema.org/draft-07/schema", // No trailing #
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"items": {
				// In Draft7, items can be an array schema.
				// In Draft2020, items is always a single schema.
				// Using definitions (Draft7) vs $defs (Draft2020) to test detection.
			},
		},
		Definitions: map[string]*jsonschema.Schema{
			"foo": {Type: "string"},
		},
	}

	// Should be recognized as Draft7 and use "definitions" not "$defs".
	err := jsonschema.Validate(schema, map[string]any{})
	require.NoError(t, err)
}

func TestDetectDraftHTTPSVariant(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft-07/schema#",
		Type:   "string",
	}

	// Should be detected as Draft7, not fall through to Draft2020.
	err := jsonschema.Validate(schema, "hello")
	require.NoError(t, err)
}

func TestResolveVocabulariesCoreNotRequired(t *testing.T) {
	t.Parallel()

	// Per spec, core vocabulary MUST be required (true) in $vocabulary.
	// Setting core to false should be rejected.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
	}

	err := jsonschema.Validate(schema, "hello",
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:       false, // core set to false -- should be an error
			jsonschema.VocabValidation2020: true,
		}),
	)
	require.Error(t, err, "core vocabulary set to false should be rejected")
}

func TestBothFormatVocabsActiveAssertsFormat(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
		Format: "email",
	}

	// Declaring both the format-annotation and format-assertion vocabularies is
	// not itself an error: the package implements no mutual-exclusion check.
	// With format-assertion active, format is asserted.
	vocabs := jsonschema.WithVocabularies(map[string]bool{
		jsonschema.VocabCore2020:             true,
		jsonschema.VocabValidation2020:       true,
		jsonschema.VocabFormatAnnotation2020: true,
		jsonschema.VocabFormatAssertion2020:  true,
	})

	// An invalid value fails specifically on the format assertion (not on any
	// nonexistent mutual-exclusion rule).
	err := jsonschema.Validate(schema, "not-an-email", vocabs)
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)
	assert.True(t, findErrorByKeyword(ve, "format"),
		"failure must be the format assertion, got: %s", err)

	// A valid value passes: declaring both vocabularies is not rejected.
	require.NoError(t, jsonschema.Validate(schema, "user@example.com", vocabs))
}

func TestDraftEnumZeroValue(t *testing.T) {
	t.Parallel()

	// The Draft zero value is Draft2020 (Draft2020 = 0, Draft7 = -1), so an
	// uninitialized Draft targets Draft 2020-12.
	var d jsonschema.Draft

	assert.Equal(t, jsonschema.Draft2020, d,
		"zero value of Draft should be Draft2020 (the documented default)")
}

func TestValidationErrorUnwrapNil(t *testing.T) {
	t.Parallel()

	// When a ref resolver returns an error, it's converted to ValidationError
	// with the error message as a string. Callers should be able to use
	// errors.Is to check for ErrRefResolve.
	schema := &jsonschema.Schema{
		Ref: "http://example.com/schema.json",
	}

	resolverErr := errors.New("network timeout")
	resolver := &errorResolver{err: resolverErr}

	err := jsonschema.Validate(schema, "hello",
		jsonschema.WithRefResolver(resolver),
	)
	require.Error(t, err)

	// Should be able to unwrap to find ErrRefResolve.
	assert.ErrorIs(t, err, jsonschema.ErrRefResolve,
		"should be able to find ErrRefResolve via errors.Is")
}

func TestDynamicScopeDeduplication(t *testing.T) {
	t.Parallel()

	// If the same base URI is re-entered through a different path,
	// the scope stack should record the re-entry (it's a stack, not a set).
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		ID:     "https://example.com/root",
		Defs: map[string]*jsonschema.Schema{
			"a": {
				DynamicAnchor: "ext",
				Type:          "string",
			},
		},
		DynamicRef: "#ext",
	}

	// This exercises the dynamic scope tracking.
	err := jsonschema.Validate(schema, "hello")
	require.NoError(t, err)
}

func TestFloat64MultipleOfRoundTrip(t *testing.T) {
	t.Parallel()

	// Both the instance and the bound convert through the shortest-decimal
	// round-trip (float64ToRat), so float64(1.01) compares as 101/100 and is a
	// clean multiple of 0.01.
	schema := &jsonschema.Schema{
		Type:       "number",
		MultipleOf: jsonschema.Ptr(0.01),
	}

	// Using Validate (not ValidateJSON) passes a float64 directly.
	err := jsonschema.Validate(schema, float64(1.01))
	require.NoError(t, err, "float64(1.01) should be a valid multiple of 0.01")
}

func TestTraverseSchemaRejectsNonIntegerIndex(t *testing.T) {
	t.Parallel()

	// A JSON Pointer index segment must be a clean integer; "0abc" is not a
	// valid index, so the ref does not resolve.
	schema := &jsonschema.Schema{
		Ref:   "#/items/0abc",
		Items: &jsonschema.Schema{Type: "string"},
	}

	// Should fail to resolve the ref since "0abc" is not a valid index.
	err := jsonschema.Validate(schema, "hello")
	require.Error(t, err, "JSON Pointer segment '0abc' should not parse as index 0")
}

// errorResolver is a test RefResolver that always returns an error.
type errorResolver struct {
	err error
}

func (r *errorResolver) ResolveRef(_ string) (*jsonschema.Schema, error) {
	return nil, r.err
}

// nilResolver always returns (nil, nil).
type nilResolver struct{}

func (r *nilResolver) ResolveRef(_ string) (*jsonschema.Schema, error) {
	//nolint:nilnil // Deliberately returns (nil, nil) to exercise the unresolved-ref path.
	return nil, nil
}

func TestValidateMutatesInputSchema(t *testing.T) {
	t.Parallel()

	// Calling Validate should not mutate the input schema.
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
		},
	}

	before, err := json.Marshal(schema)
	require.NoError(t, err)

	err = jsonschema.Validate(schema, map[string]any{"name": "test"})
	require.NoError(t, err)

	after, err := json.Marshal(schema)
	require.NoError(t, err)

	assert.JSONEq(t, string(before), string(after),
		"Validate should not mutate the input schema")
}

func TestRemoteLoaderReturnsEmptyOnFailure(t *testing.T) {
	t.Parallel()

	// When refResolver is nil, remoteLoader returns &Schema{} (accepts all).
	// A broken remote ref should not silently validate as accepting all values.
	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"data": {Ref: "http://example.com/nonexistent-schema.json"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"data": 42})
	// Should produce an error because the $ref cannot be resolved.
	require.Error(t, err, "unresolvable remote $ref should not silently accept all values")
}

func TestRegexCacheKeyedByPattern(t *testing.T) {
	t.Parallel()

	// The package-level regexCache keys compiled patterns by their source, so
	// each distinct pattern validates against its own regex rather than a stale
	// cached one. Across 26 single-letter patterns only "^testA$" matches the
	// instance "testA"; a mis-keyed cache would let another letter's pattern
	// match it.
	for i := range 26 {
		letter := string(rune('A' + i))
		schema := &jsonschema.Schema{
			Type:    "string",
			Pattern: "^test" + letter + "$",
		}

		err := jsonschema.Validate(schema, "testA")
		if letter == "A" {
			require.NoError(t, err, "pattern %q should match %q", schema.Pattern, "testA")
		} else {
			require.Error(t, err, "pattern %q should not match %q", schema.Pattern, "testA")
		}
	}
}

func TestCycleDetectionPointerIdentity(t *testing.T) {
	t.Parallel()

	// If two different *Schema pointers represent the same logical schema
	// (e.g., after cloneSchema), cycles involving the clone won't be detected.
	schema := &jsonschema.Schema{
		ID: "https://example.com/root",
		Properties: map[string]*jsonschema.Schema{
			"child": {Ref: "https://example.com/root"},
		},
	}

	// Create a resolver that returns a clone of the same schema.
	resolver := &cloningResolver{schema: schema}

	err := jsonschema.Validate(schema, map[string]any{
		"child": map[string]any{
			"child": map[string]any{},
		},
	}, jsonschema.WithRefResolver(resolver))
	// Should not infinite-loop; cycle detection should recognize the clone.
	require.NoError(t, err)
}

type cloningResolver struct {
	schema *jsonschema.Schema
}

func (r *cloningResolver) ResolveRef(_ string) (*jsonschema.Schema, error) {
	// Return a different pointer to the same logical schema. A round-trip of a
	// known-good schema cannot fail, so the errors are deliberately ignored.
	data, _ := json.Marshal(r.schema) //nolint:errcheck,errchkjson // Round-tripping a known-good schema.

	var cp jsonschema.Schema

	_ = json.Unmarshal(data, &cp) //nolint:errcheck // Round-tripping a known-good schema.

	return &cp, nil
}

func TestIsEmptySchemaExtraField(t *testing.T) {
	t.Parallel()

	// A schema whose only keyword is an unknown one (carried in Extra) sets no
	// validation constraints, so it accepts every instance.
	schema := &jsonschema.Schema{
		Extra: map[string]any{
			"x-custom": true,
		},
	}

	err := jsonschema.Validate(schema, 42.0)
	require.NoError(t, err)
}

func TestRemoteLoaderDoubleCall(t *testing.T) {
	t.Parallel()

	// The RefResolver should be called at most once per URI.
	var callCount atomic.Int64

	resolver := &countingRefResolver{
		count:  &callCount,
		schema: &jsonschema.Schema{Type: "string"},
	}

	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"data": {Ref: "http://example.com/string-schema.json"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"data": "hello"},
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	assert.Equal(t, int64(1), callCount.Load(),
		"RefResolver should be called at most once per URI")
}

// countingRefResolver counts ResolveRef calls atomically so the test is
// race-free regardless of how the validator schedules ref resolution.
type countingRefResolver struct {
	count  *atomic.Int64
	schema *jsonschema.Schema
}

func (r *countingRefResolver) ResolveRef(_ string) (*jsonschema.Schema, error) {
	r.count.Add(1)

	return r.schema, nil
}

func TestWalkSchemaSkipsRefs(t *testing.T) {
	t.Parallel()

	// Anchors defined inside a remotely-resolved schema — and that document's own
	// sub-$ref to them — are registered and usable, because resolveRemote walks
	// the fetched document before use. WalkSchema itself does not follow $ref;
	// the registration happens when the remote document is walked on fetch.
	remote := &jsonschema.Schema{
		// The remote doc's own root $ref points at an $anchor it defines, so the
		// anchor is reachable only after walkSchema registers the fetched remote.
		ID:  "http://example.com/remote.json",
		Ref: "http://example.com/remote.json#thing",
		Defs: map[string]*jsonschema.Schema{
			"thing": {Anchor: "thing", Type: "integer"},
		},
	}
	root := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Properties: map[string]*jsonschema.Schema{
			"data": {Ref: "http://example.com/remote.json"},
		},
	}
	resolver := mapResolver{"http://example.com/remote.json": remote}

	// An integer satisfies the anchor reached through the remote sub-$ref.
	err := jsonschema.Validate(root, map[string]any{"data": 42.0},
		jsonschema.WithRefResolver(resolver))
	require.NoError(t, err,
		"$anchor inside a remotely-resolved schema should register and resolve")

	// A non-integer fails, proving the anchored constraint is actually applied
	// (not silently skipped as unresolved).
	err = jsonschema.Validate(root, map[string]any{"data": "nope"},
		jsonschema.WithRefResolver(resolver))
	require.Error(t, err,
		"the integer constraint behind the remote anchor must be enforced")
}

func TestTraverseSchemaEmptySegments(t *testing.T) {
	t.Parallel()

	// JSON Pointer #/ (empty segment) should reference the member named ""
	// (empty string) of the root, per RFC 6901.
	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"": {Type: "string"}, // Property with empty string key
		},
		Defs: map[string]*jsonschema.Schema{
			"check": {Ref: "#/properties/"},
		},
	}

	// The $ref "#/properties/" should resolve to the "" property.
	err := jsonschema.Validate(schema, map[string]any{"": "hello"})
	require.NoError(t, err)
}

func TestResolveJSONPointerDoubleDecoding(t *testing.T) {
	t.Parallel()

	// The url.Parse call already decodes the fragment; applying PathUnescape again
	// could double-decode. A literal %2F in a property name would be
	// decoded to / by PathUnescape, incorrectly splitting the pointer.
	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"a/b": {Type: "string"}, // Property name contains literal /
		},
		Defs: map[string]*jsonschema.Schema{
			"check": {Ref: "#/properties/a~1b"}, // ~1 is JSON Pointer escape for /
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"a/b": "hello"})
	require.NoError(t, err)
}

func TestTraverseSchemaCrossDraftConfusion(t *testing.T) {
	t.Parallel()

	// In 2020-12, items is always a single schema. A ref like #/items/0
	// should fail since 2020-12 items is not an array.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Items:  &jsonschema.Schema{Type: "string"},
		Defs: map[string]*jsonschema.Schema{
			"check": {Ref: "#/items/0"},
		},
	}

	// #/items/0 should NOT resolve in 2020-12 since items is a single schema.
	// The ref should fail to resolve since you can't index into a single schema.
	err := jsonschema.Validate(schema, []any{"hello"})
	require.Error(t, err,
		"#/items/0 should not resolve in 2020-12 where items is a single schema, not an array")
}

func TestCustomFormatVocabularyBypass(t *testing.T) {
	t.Parallel()

	// A custom format checker is gated by the same format-assertion resolution
	// as the built-in checkers: with only the format-annotation vocabulary
	// active it is annotation-only and produces no validation error.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
		Format: "custom-format",
	}

	customChecker := func(s string) error {
		if s == "bad" {
			return errors.New("invalid custom format")
		}

		return nil
	}

	// With format-annotation vocabulary only, custom formats should NOT assert.
	err := jsonschema.Validate(schema, "bad",
		jsonschema.WithFormatValidator("custom-format", customChecker),
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:             true,
			jsonschema.VocabValidation2020:       true,
			jsonschema.VocabFormatAnnotation2020: true,
			// The format-assertion vocabulary is NOT active.
		}),
	)
	require.NoError(t, err,
		"custom format should respect format-annotation vocabulary and not assert")
}

func TestDynamicRefBookendingCheck(t *testing.T) {
	t.Parallel()

	// $dynamicRef "bookending": when the static target carries a matching
	// $dynamicAnchor, resolution walks the dynamic scope outermost->innermost and
	// binds to the FIRST matching $dynamicAnchor instead of the static target.
	// This mirrors suite case "A $dynamicRef that initially resolves to a schema
	// with a matching $dynamicAnchor resolves to the first $dynamicAnchor in the
	// dynamic scope". The "withBookend" schema places a $dynamicAnchor on the root
	// so the recursive "baz" subtree is validated against the root (which requires
	// foo == "pass"); the "withoutBookend" schema omits the root $dynamicAnchor so
	// the same $dynamicRef degrades to its static target ("extended"), which has no
	// foo constraint. The two reacting differently to the same instance is what
	// proves the bookending walk, not plain static $ref resolution, is in effect.
	defs := func() map[string]*jsonschema.Schema {
		return map[string]*jsonschema.Schema{
			"extended": {
				ID:            "extended",
				DynamicAnchor: "meta",
				Type:          "object",
				Properties: map[string]*jsonschema.Schema{
					"bar": {Ref: "bar"},
				},
			},
			"bar": {
				ID:   "bar",
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"baz": {DynamicRef: "extended#meta"},
				},
			},
		}
	}

	withBookend := &jsonschema.Schema{
		Schema:        "https://json-schema.org/draft/2020-12/schema",
		ID:            "https://test.example/bookend/root",
		DynamicAnchor: "meta", // outermost bookend
		Type:          "object",
		Properties: map[string]*jsonschema.Schema{
			"foo": {Const: jsonschema.Ptr[any]("pass")},
		},
		Ref:  "extended",
		Defs: defs(),
	}

	withoutBookend := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		ID:     "https://test.example/nobookend/root",
		// No root $dynamicAnchor: nothing to bookend to, so $dynamicRef stays static.
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"foo": {Const: jsonschema.Ptr[any]("pass")},
		},
		Ref:  "extended",
		Defs: defs(),
	}

	recursivePass := map[string]any{
		"foo": "pass",
		"bar": map[string]any{"baz": map[string]any{"foo": "pass"}},
	}
	recursiveFail := map[string]any{
		"foo": "pass",
		"bar": map[string]any{"baz": map[string]any{"foo": "fail"}},
	}

	// With the bookend, the nested "baz" is validated against the root, so a
	// nested foo == "pass" passes and foo == "fail" fails.
	require.NoError(t, jsonschema.Validate(withBookend, recursivePass),
		"nested value matching the root bookend should validate")
	require.Error(t, jsonschema.Validate(withBookend, recursiveFail),
		"bookending must validate the nested value against the root $dynamicAnchor")

	// Without the bookend the same $dynamicRef resolves statically to "extended",
	// which imposes no foo constraint, so the otherwise-failing instance passes.
	// This control confirms the difference is the dynamic-scope walk, not $ref.
	require.NoError(t, jsonschema.Validate(withoutBookend, recursiveFail),
		"without a root bookend, $dynamicRef degrades to its static target")
}

func TestResolveRefURNStyle(t *testing.T) {
	t.Parallel()

	// URI resolution via url.ResolveReference uses hierarchical semantics
	// that differ from URN resolution. Relative resolution against URN bases
	// would produce incorrect results.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		ID:     "urn:example:schema",
		Defs: map[string]*jsonschema.Schema{
			"sub": {
				Type: "string",
			},
		},
		Ref: "#/$defs/sub",
	}

	err := jsonschema.Validate(schema, "hello")
	require.NoError(t, err)
}

func TestDraft07ItemsSingleSchema(t *testing.T) {
	t.Parallel()

	// In Draft-07, items may be a single schema that applies to every array
	// element (as opposed to the tuple form where items is an array of schemas).
	// Assert that single-schema items validates each element uniformly.
	schema := &jsonschema.Schema{
		Schema: "http://json-schema.org/draft-07/schema#",
		Items:  &jsonschema.Schema{Type: "string"},
	}

	// Every element is a string: valid.
	err := jsonschema.Validate(schema, []any{"a", "b"})
	require.NoError(t, err, "all-string array should satisfy items: {type: string}")

	// A non-string element fails, and the error points at that element's index.
	err = jsonschema.Validate(schema, []any{"a", 1.0})
	require.Error(t, err, "a non-string element must violate items: {type: string}")

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)
	assert.True(t, findError(ve, "type", "/1"),
		"expected a type error at element index 1, got: %s", err)
}

func TestRegistryResolvesAnchorAfterResolve(t *testing.T) {
	t.Parallel()

	// BuildRegistry runs before Schema.Resolve, yet the registry stays current
	// for well-formed schemas. Whether Resolve adds registry entries depends on
	// unexported upstream internals that can't be observed directly, so the test
	// asserts the consequence callers care about: for a schema carrying $id,
	// $anchor, and a $ref to that anchor, the full Validate pipeline resolves the
	// anchor and enforces the anchored constraints.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		ID:     "https://example.com/registry-root",
		Defs: map[string]*jsonschema.Schema{
			"named": {
				Anchor:    "namedAnchor",
				Type:      "string",
				MinLength: jsonschema.Ptr(2),
			},
		},
		Ref: "#namedAnchor",
	}

	// A string of length >= 2 satisfies the anchored constraints.
	require.NoError(t, jsonschema.Validate(schema, "hello"),
		"#namedAnchor should resolve to the $defs schema after the full pipeline")

	// Wrong type: the anchored type constraint must still apply.
	require.Error(t, jsonschema.Validate(schema, 42.0),
		"anchored type constraint must be enforced (registry not stale)")

	// Right type but too short: the anchored minLength must still apply.
	require.Error(t, jsonschema.Validate(schema, "x"),
		"anchored minLength constraint must be enforced (registry not stale)")
}

func TestFormatsEnabledTriState(t *testing.T) {
	t.Parallel()

	// Format assertion under Draft 2020-12 is tri-state: unset follows the
	// vocabulary (annotation-only under the standard meta-schema, per §7.2.1),
	// while an explicit WithFormats forces it on or off regardless.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
		Format: "email",
	}

	tests := map[string]struct {
		opts      []jsonschema.ValidateOption
		assertErr bool
	}{
		"bare 2020-12 default is annotation-only": {
			opts:      nil,
			assertErr: false,
		},
		"WithFormats(true) forces assertion": {
			opts:      []jsonschema.ValidateOption{jsonschema.WithFormats(true)},
			assertErr: true,
		},
		"WithFormats(false) disables assertion": {
			opts:      []jsonschema.ValidateOption{jsonschema.WithFormats(false)},
			assertErr: false,
		},
		"format-annotation vocabulary only is annotation-only": {
			opts: []jsonschema.ValidateOption{
				jsonschema.WithVocabularies(map[string]bool{
					jsonschema.VocabCore2020:             true,
					jsonschema.VocabValidation2020:       true,
					jsonschema.VocabFormatAnnotation2020: true,
				}),
			},
			assertErr: false,
		},
		"format-assertion vocabulary asserts": {
			opts: []jsonschema.ValidateOption{
				jsonschema.WithVocabularies(map[string]bool{
					jsonschema.VocabCore2020:            true,
					jsonschema.VocabValidation2020:      true,
					jsonschema.VocabFormatAssertion2020: true,
				}),
			},
			assertErr: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(schema, "not-an-email", tt.opts...)
			if tt.assertErr {
				require.Error(t, err, "expected format to be asserted")
				assert.Contains(t, err.Error(), "format")
			} else {
				require.NoError(t, err, "expected format to be annotation-only")
			}
		})
	}
}

func TestResolveRemoteClonesConsistently(t *testing.T) {
	t.Parallel()

	// Both remote-resolution paths deep-copy before registering: remoteLoader
	// (during Schema.Resolve) and resolveRemote (the validation-walk fallback).
	// The cache therefore always holds an independent copy, so neither upstream
	// nor walk mutations ever touch the resolver-owned schema.
	original := &jsonschema.Schema{
		Type:        "string",
		Description: "original",
	}

	resolver := &mutatingResolver{schema: original}

	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"a": {Ref: "http://example.com/s.json"},
			"b": {Ref: "http://example.com/s.json"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"a": "hello", "b": "world"},
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	// The original schema should not have been mutated by the validator.
	assert.Equal(t, "original", original.Description,
		"resolver-returned schema should not be mutated by caching")
}

type mutatingResolver struct {
	schema *jsonschema.Schema
}

func (r *mutatingResolver) ResolveRef(_ string) (*jsonschema.Schema, error) {
	return r.schema, nil
}

// The conformance suite drives ValidateJSON (the json.Number path); the tests
// below cover the Validate() float64 path and other entry points the suite does
// not exercise.

func TestValidateFloat64PathForNumericKeywords(t *testing.T) {
	t.Parallel()

	// The suite only uses ValidateJSON (json.Number path). This exercises the
	// Validate() float64 path for all numeric keywords.
	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance float64
		err      bool
	}{
		"minimum pass": {
			schema:   &jsonschema.Schema{Type: "number", Minimum: jsonschema.Ptr(1.0)},
			instance: 2.0,
		},
		"minimum fail": {
			schema:   &jsonschema.Schema{Type: "number", Minimum: jsonschema.Ptr(5.0)},
			instance: 3.0,
			err:      true,
		},
		"maximum pass": {
			schema:   &jsonschema.Schema{Type: "number", Maximum: jsonschema.Ptr(10.0)},
			instance: 5.0,
		},
		"maximum fail": {
			schema:   &jsonschema.Schema{Type: "number", Maximum: jsonschema.Ptr(3.0)},
			instance: 5.0,
			err:      true,
		},
		"exclusiveMinimum pass": {
			schema:   &jsonschema.Schema{Type: "number", ExclusiveMinimum: jsonschema.Ptr(1.0)},
			instance: 2.0,
		},
		"exclusiveMinimum fail": {
			schema:   &jsonschema.Schema{Type: "number", ExclusiveMinimum: jsonschema.Ptr(5.0)},
			instance: 5.0,
			err:      true,
		},
		"exclusiveMaximum pass": {
			schema:   &jsonschema.Schema{Type: "number", ExclusiveMaximum: jsonschema.Ptr(10.0)},
			instance: 5.0,
		},
		"exclusiveMaximum fail": {
			schema:   &jsonschema.Schema{Type: "number", ExclusiveMaximum: jsonschema.Ptr(5.0)},
			instance: 5.0,
			err:      true,
		},
		"multipleOf pass": {
			schema:   &jsonschema.Schema{Type: "number", MultipleOf: jsonschema.Ptr(3.0)},
			instance: 9.0,
		},
		"multipleOf fail": {
			schema:   &jsonschema.Schema{Type: "number", MultipleOf: jsonschema.Ptr(3.0)},
			instance: 10.0,
			err:      true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tc.schema, tc.instance)
			if tc.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVocabularyGatedBehavior(t *testing.T) {
	t.Parallel()

	// Test that disabling specific vocabularies actually changes validation behavior.
	// The existing tests don't exercise custom vocabulary configurations.

	// With validation vocabulary disabled, type checks should not apply.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "string",
		Properties: map[string]*jsonschema.Schema{
			"a": {Type: "integer"},
		},
	}

	// Normally this would fail (42 is not a string).
	err := jsonschema.Validate(schema, 42.0,
		jsonschema.WithVocabularies(map[string]bool{
			jsonschema.VocabCore2020:       true,
			jsonschema.VocabApplicator2020: true,
			// Validation vocabulary NOT active -- type should not be checked.
		}),
	)
	require.NoError(t, err,
		"with validation vocabulary disabled, type keyword should not be enforced")
}

func TestSchemaUnrecognizedURI(t *testing.T) {
	t.Parallel()

	// An unrecognized $schema URI falls back to Draft2020, so a valid instance
	// still validates.
	schema := &jsonschema.Schema{
		Schema: "https://example.com/unknown-draft",
		Type:   "string",
	}

	err := jsonschema.Validate(schema, "hello")
	require.NoError(t, err)
}

func TestDynamicRefHandCrafted(t *testing.T) {
	t.Parallel()

	// $dynamicRef is only tested via the suite runner (ValidateJSON path).
	// This tests it via Validate() directly.
	schema := &jsonschema.Schema{
		Schema:     "https://json-schema.org/draft/2020-12/schema",
		ID:         "https://example.com/root",
		DynamicRef: "#ext",
		Defs: map[string]*jsonschema.Schema{
			"base": {
				DynamicAnchor: "ext",
				Type:          "string",
			},
		},
	}

	err := jsonschema.Validate(schema, "hello")
	require.NoError(t, err)

	err = jsonschema.Validate(schema, 42.0)
	require.Error(t, err, "$dynamicRef should enforce the resolved schema")
}

func TestKeywordAndInstancePathTogether(t *testing.T) {
	t.Parallel()

	// No existing test verifies both keyword AND instance path in the same assertion.
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string", MinLength: jsonschema.Ptr(3)},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"name": "ab"})
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	// Find the minLength error and verify both keyword and path.
	found := findError(ve, "minLength", "/name")
	assert.True(t, found,
		"should find a minLength error at /name, got: %s", err)
}

// findError recursively searches a ValidationError tree for a specific keyword + path.
func findError(ve *jsonschema.ValidationError, keyword, path string) bool {
	if ve.Keyword == keyword && ve.InstancePath == path {
		return true
	}

	for _, cause := range ve.Causes {
		if findError(cause, keyword, path) {
			return true
		}
	}

	return false
}

// findErrorByKeyword recursively searches a ValidationError tree for a specific keyword
// at any instance path. Use this when the exact path is not relevant to the test.
func findErrorByKeyword(ve *jsonschema.ValidationError, keyword string) bool {
	if ve.Keyword == keyword {
		return true
	}

	for _, cause := range ve.Causes {
		if findErrorByKeyword(cause, keyword) {
			return true
		}
	}

	return false
}

func TestJSONNumberAcrossNumericKeywords(t *testing.T) {
	t.Parallel()

	// The json.Number type is tested in only 2 of 12 numeric test cases.
	tests := map[string]struct {
		schema *jsonschema.Schema
		data   string
		err    bool
	}{
		"multipleOf pass": {
			schema: &jsonschema.Schema{Type: "number", MultipleOf: jsonschema.Ptr(3.0)},
			data:   `9`,
		},
		"multipleOf fail": {
			schema: &jsonschema.Schema{Type: "number", MultipleOf: jsonschema.Ptr(3.0)},
			data:   `10`,
			err:    true,
		},
		"exclusiveMinimum pass": {
			schema: &jsonschema.Schema{Type: "number", ExclusiveMinimum: jsonschema.Ptr(5.0)},
			data:   `6`,
		},
		"exclusiveMinimum fail": {
			schema: &jsonschema.Schema{Type: "number", ExclusiveMinimum: jsonschema.Ptr(5.0)},
			data:   `5`,
			err:    true,
		},
		"exclusiveMaximum pass": {
			schema: &jsonschema.Schema{Type: "number", ExclusiveMaximum: jsonschema.Ptr(10.0)},
			data:   `9`,
		},
		"exclusiveMaximum fail": {
			schema: &jsonschema.Schema{Type: "number", ExclusiveMaximum: jsonschema.Ptr(10.0)},
			data:   `10`,
			err:    true,
		},
		"maximum pass": {
			schema: &jsonschema.Schema{Type: "number", Maximum: jsonschema.Ptr(10.0)},
			data:   `10`,
		},
		"maximum fail": {
			schema: &jsonschema.Schema{Type: "number", Maximum: jsonschema.Ptr(10.0)},
			data:   `11`,
			err:    true,
		},
		"enum with mixed types": {
			schema: &jsonschema.Schema{Enum: []any{1.0, "hello", true}},
			data:   `1`,
		},
		"uniqueItems cross-type equality": {
			schema: &jsonschema.Schema{Type: "array", UniqueItems: true},
			data:   `[1, 1.0]`,
			err:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.ValidateJSON(tc.schema, []byte(tc.data))
			if tc.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUniqueItemsNumericRepresentation(t *testing.T) {
	t.Parallel()

	// UniqueItems must agree with jsonschema.Equal's big.Rat comparison even when
	// the same number appears in different Go representations. This is reachable
	// only through Validate; ValidateJSON produces json.Number uniformly.
	tests := map[string]struct {
		instance []any
		err      bool
	}{
		"small integer mixed representations": {
			instance: []any{float64(5), json.Number("5")},
			err:      true,
		},
		"integer at 2^63 mixed representations": {
			// 2^63 is exactly representable as float64 but exceeds int64, so the
			// hash falls back to an exact key and recognizes the float64 and
			// json.Number forms as the same value.
			instance: []any{float64(9223372036854775808), json.Number("9223372036854775808")},
			err:      true,
		},
		"binary and decimal fractions are distinct": {
			// Float64(0.1) is the exact binary 0.1000...0555, not the rational
			// 1/10 that json.Number("0.1") denotes, so they are not duplicates.
			instance: []any{float64(0.1), json.Number("0.1")},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{Type: "array", UniqueItems: true}
			err := jsonschema.Validate(schema, tt.instance)
			if tt.err {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "uniqueItems")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDeeplyNestedErrors(t *testing.T) {
	t.Parallel()

	// No test verifies error paths at 3+ levels, through arrays, or through
	// multiple $ref traversals.
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"level1": {
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"level2": {
						Type: "object",
						Properties: map[string]*jsonschema.Schema{
							"level3": {Type: "string", MinLength: jsonschema.Ptr(1)},
						},
					},
				},
			},
		},
	}

	instance := map[string]any{
		"level1": map[string]any{
			"level2": map[string]any{
				"level3": "",
			},
		},
	}

	err := jsonschema.Validate(schema, instance)
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	// Use structured matching to verify the 3-level path.
	found := findError(ve, "minLength", "/level1/level2/level3")
	assert.True(t, found,
		"error should report the full 3-level instance path /level1/level2/level3, got: %s", err)
}

func TestLocalAnchorResolution(t *testing.T) {
	t.Parallel()

	// The existing $anchor test uses a remote resolver. This tests $anchor
	// defined within the root schema itself.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		ID:     "https://example.com/local-anchor",
		Ref:    "#myAnchor",
		Defs: map[string]*jsonschema.Schema{
			"myType": {
				Anchor: "myAnchor",
				Type:   "integer",
			},
		},
	}

	err := jsonschema.Validate(schema, 42.0)
	require.NoError(t, err, "local $anchor resolution should work")

	err = jsonschema.Validate(schema, "not an integer")
	require.Error(t, err, "local $anchor should enforce the resolved schema")
}

func TestDependentSchemasWithTypeCheck(t *testing.T) {
	t.Parallel()

	// Existing tests only use Required inside the dependent schema.
	// This tests with type checks and composition keywords.
	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      bool
	}{
		"dependent schema with type check pass": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependentSchemas: map[string]*jsonschema.Schema{
					"credit_card": {
						Properties: map[string]*jsonschema.Schema{
							"billing_address": {Type: "string", MinLength: jsonschema.Ptr(1)},
						},
						Required: []string{"billing_address"},
					},
				},
			},
			instance: map[string]any{
				"credit_card":     "1234",
				"billing_address": "123 Main St",
			},
		},
		"dependent schema with type check fail": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependentSchemas: map[string]*jsonschema.Schema{
					"credit_card": {
						Properties: map[string]*jsonschema.Schema{
							"billing_address": {Type: "string", MinLength: jsonschema.Ptr(1)},
						},
						Required: []string{"billing_address"},
					},
				},
			},
			instance: map[string]any{
				"credit_card":     "1234",
				"billing_address": "", // Too short
			},
			err: true,
		},
		"dependent schema not triggered when property absent": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependentSchemas: map[string]*jsonschema.Schema{
					"credit_card": {
						Required: []string{"billing_address"},
					},
				},
			},
			instance: map[string]any{
				"name": "Alice", // No credit_card, so dependent schema not applied.
			},
		},
		"dependent schema with composition keyword": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "object",
				DependentSchemas: map[string]*jsonschema.Schema{
					"payment_type": {
						AnyOf: []*jsonschema.Schema{
							{Required: []string{"card_number"}},
							{Required: []string{"bank_account"}},
						},
					},
				},
			},
			instance: map[string]any{
				"payment_type": "card",
				// Neither card_number nor bank_account.
			},
			err: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tc.schema, tc.instance)
			if tc.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCircularRefErrorAtDepth(t *testing.T) {
	t.Parallel()

	// Existing TestValidateCircularRef only tests valid data.
	// This tests an instance that violates the schema at depth 3+.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Ref:    "#/$defs/node",
		Defs: map[string]*jsonschema.Schema{
			"node": {
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"value": {Type: "string"},
					"children": {
						Type:  "array",
						Items: &jsonschema.Schema{Ref: "#/$defs/node"},
					},
				},
			},
		},
	}

	instance := map[string]any{
		"value": "root",
		"children": []any{
			map[string]any{
				"value": "child1",
				"children": []any{
					map[string]any{
						"value": 42.0, // Wrong type at depth 3
					},
				},
			},
		},
	}

	err := jsonschema.Validate(schema, instance)
	require.Error(t, err, "deep validation errors should bubble up through recursive refs")

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	found := findErrorByKeyword(ve, "type")
	assert.True(t, found,
		"error should identify the type mismatch at depth 3, got: %s", err)
}

func TestValidateJSONStructuredError(t *testing.T) {
	t.Parallel()

	// No test verifies that ValidateJSON returns *ValidationError extractable
	// via ErrorAs with InstancePath and SchemaPath fields.
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"count": {Type: "integer"},
		},
	}

	err := jsonschema.ValidateJSON(schema, []byte(`{"count": "not a number"}`))
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	// Find the type error at /count.
	found := findError(ve, "type", "/count")
	assert.True(t, found,
		"ValidateJSON should produce structured error with keyword and path, got: %s", err)
}

func TestRemoteRefNilResolverIsError(t *testing.T) {
	t.Parallel()

	// A resolver that returns (nil, nil) for an unknown URI leaves the $ref
	// unresolved, which is a validation error rather than a silent pass.
	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"data": {Ref: "http://example.com/completely-wrong-uri.json"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"data": 42},
		jsonschema.WithRefResolver(&nilResolver{}),
	)
	require.Error(t, err,
		"nil resolver returning (nil, nil) should not silently pass validation for unresolvable $ref")
}

func TestRefToRootReportsNestedInstancePath(t *testing.T) {
	t.Parallel()

	// A $ref:"#" recursion reports the error at the nested instance location
	// (/child/name), not at the top-level property.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"name":  {Type: "string"},
			"child": {Ref: "#"},
		},
	}

	instance := map[string]any{
		"name": "root",
		"child": map[string]any{
			"name": 42.0, // wrong type at /child/name
		},
	}

	err := jsonschema.Validate(schema, instance)
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	// The error should report /child/name, not just /name.
	found := findError(ve, "type", "/child/name")
	assert.True(t, found,
		"error from $ref:'#' should report InstancePath /child/name, got: %s", err)
}

func TestVocabularyViaMetaSchemaPath(t *testing.T) {
	t.Parallel()

	// The existing test uses WithVocabularies to inject unknown vocabulary.
	// This tests the same via WithMetaSchema (the $vocabulary in a metaschema).
	metaschema := &jsonschema.Schema{
		ID: "https://example.com/custom-meta",
		Vocabulary: map[string]bool{
			jsonschema.VocabCore2020:                     true,
			"https://example.com/unknown-required-vocab": true, // unknown, required
		},
	}

	schema := &jsonschema.Schema{
		Schema: "https://example.com/custom-meta",
		Type:   "string",
	}

	err := jsonschema.Validate(schema, "hello",
		jsonschema.WithMetaSchema(metaschema),
	)
	// Should fail with ErrUnknownVocabulary via the metaschema path.
	require.ErrorIs(t, err, jsonschema.ErrUnknownVocabulary,
		"unknown required vocabulary via WithMetaSchema should be rejected")
}

// TestFormatAssertion exercises the built-in format checkers as assertions.
// Under Draft 2020-12 format is annotation-only by default, so each case opts
// in with WithFormats(true). Every built-in format is checked with both a
// representative valid instance (must pass) and an invalid one (must fail),
// giving direct coverage of format-as-assertion outside the optional suite.
func TestFormatAssertion(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format  string
		valid   string
		invalid string
	}{
		"date-time": {
			format:  "date-time",
			valid:   "2024-01-01T12:00:00Z",
			invalid: "not-a-date",
		},
		"date": {
			format:  "date",
			valid:   "2024-01-01",
			invalid: "01/01/2024",
		},
		"time": {
			format:  "time",
			valid:   "12:00:00Z",
			invalid: "25:00:00",
		},
		"duration": {
			format:  "duration",
			valid:   "P1Y2M3D",
			invalid: "P",
		},
		"email": {
			format:  "email",
			valid:   "test@example.com",
			invalid: "not-an-email",
		},
		"idn-email": {
			format:  "idn-email",
			valid:   "test@example.com",
			invalid: "no-at-sign",
		},
		"hostname": {
			format:  "hostname",
			valid:   "example.com",
			invalid: "-invalid.com",
		},
		"idn-hostname": {
			format:  "idn-hostname",
			valid:   "example.com",
			invalid: "-bad-.com",
		},
		"ipv4": {
			format:  "ipv4",
			valid:   "192.168.1.1",
			invalid: "999.999.999.999",
		},
		"ipv6": {
			format:  "ipv6",
			valid:   "::1",
			invalid: "not-ipv6",
		},
		"uri": {
			format:  "uri",
			valid:   "https://example.com",
			invalid: "example.com", // relative reference, missing scheme
		},
		"uri-reference": {
			format:  "uri-reference",
			valid:   "/path/to/resource",
			invalid: "http://exa mple.com", // forbidden space character
		},
		"uri-template": {
			format:  "uri-template",
			valid:   "/a/{id}",
			invalid: "/a/{id", // unbalanced brace
		},
		"iri": {
			format:  "iri",
			valid:   "https://example.com",
			invalid: "noscheme",
		},
		"iri-reference": {
			format:  "iri-reference",
			valid:   "/path",
			invalid: "http://exa mple.com", // forbidden space character
		},
		"uuid": {
			format:  "uuid",
			valid:   "550e8400-e29b-41d4-a716-446655440000",
			invalid: "not-a-uuid",
		},
		"json-pointer": {
			format:  "json-pointer",
			valid:   "/foo/bar",
			invalid: "not/a/pointer", // must start with '/'
		},
		"relative-json-pointer": {
			format:  "relative-json-pointer",
			valid:   "1/foo",
			invalid: "/foo", // must start with a non-negative integer
		},
		"regex": {
			format:  "regex",
			valid:   "^[a-z]+$",
			invalid: "[invalid", // unterminated character class
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// 2020-12 treats format as annotation-only by default, so opt into
			// assertion behavior explicitly via WithFormats(true).
			schema := &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Type:   "string",
				Format: tt.format,
			}

			require.NoError(t,
				jsonschema.Validate(schema, tt.valid, jsonschema.WithFormats(true)),
				"valid %s instance %q should pass", tt.format, tt.valid)

			err := jsonschema.Validate(schema, tt.invalid, jsonschema.WithFormats(true))
			require.Error(t, err,
				"invalid %s instance %q should fail", tt.format, tt.invalid)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.True(t, findErrorByKeyword(ve, "format"),
				"failure should be attributed to the format keyword")
		})
	}
}

func TestBooleanSchemaRepresentation(t *testing.T) {
	t.Parallel()

	// The unmarshalTestSchema helper converts JSON false to Schema{Not: &Schema{}}.
	// This works but means isFalseSchema detection is tightly coupled to
	// this specific representation. If a user constructs a false schema
	// differently, detection could fail.
	falseSchema := &jsonschema.Schema{Not: &jsonschema.Schema{}}
	err := jsonschema.Validate(falseSchema, "anything")
	require.Error(t, err, "false schema should reject all instances")
}

// falseSchema returns the boolean false schema in the parsed form the
// upstream library uses, an empty not subschema.
func falseSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Not: &jsonschema.Schema{}}
}

// TestFalseSchemaKeyword pins that a violation of a false subschema carries
// the applicator keyword that applied it, so a consumer can tell an
// additionalProperties:false failure from a false property or item subschema
// without parsing SchemaPath. A standalone false schema has no applicator
// context and keeps an empty Keyword.
func TestFalseSchemaKeyword(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema       *jsonschema.Schema
		instance     any
		keyword      string
		instancePath string
		schemaPath   string
	}{
		"additionalProperties false": {
			schema: &jsonschema.Schema{
				Type:                 "object",
				AdditionalProperties: falseSchema(),
			},
			instance:     map[string]any{"extra": 1.0},
			keyword:      "additionalProperties",
			instancePath: "/extra",
			schemaPath:   "/additionalProperties",
		},
		"false property subschema": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"forbidden": falseSchema(),
				},
			},
			instance:     map[string]any{"forbidden": 1.0},
			keyword:      "properties",
			instancePath: "/forbidden",
			schemaPath:   "/properties/forbidden",
		},
		"false patternProperties subschema": {
			schema: &jsonschema.Schema{
				Type: "object",
				PatternProperties: map[string]*jsonschema.Schema{
					"^x-": falseSchema(),
				},
			},
			instance:     map[string]any{"x-a": 1.0},
			keyword:      "patternProperties",
			instancePath: "/x-a",
			schemaPath:   "/patternProperties/^x-",
		},
		"items false": {
			schema: &jsonschema.Schema{
				Type:     "array",
				Items:    falseSchema(),
				MaxItems: jsonschema.Ptr(10),
			},
			instance:     []any{1.0},
			keyword:      "items",
			instancePath: "/0",
			schemaPath:   "/items",
		},
		"prefixItems false": {
			schema: &jsonschema.Schema{
				Type:        "array",
				PrefixItems: []*jsonschema.Schema{falseSchema()},
			},
			instance:     []any{1.0},
			keyword:      "prefixItems",
			instancePath: "/0",
			schemaPath:   "/prefixItems/0",
		},
		"draft7 additionalItems false": {
			schema: &jsonschema.Schema{
				Schema:          "http://json-schema.org/draft-07/schema#",
				Type:            "array",
				ItemsArray:      []*jsonschema.Schema{{Type: "number"}},
				AdditionalItems: falseSchema(),
			},
			instance:     []any{1.0, 2.0},
			keyword:      "additionalItems",
			instancePath: "/1",
			schemaPath:   "/additionalItems",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)

			assert.Equal(t, tt.keyword, ve.Keyword)
			assert.Equal(t, tt.instancePath, ve.InstancePath)
			assert.Equal(t, tt.schemaPath, ve.SchemaPath)
			assert.Equal(t, "value is not allowed", ve.Message)
			assert.Empty(t, ve.Causes)
		})
	}

	t.Run("standalone false schema keeps empty keyword", func(t *testing.T) {
		t.Parallel()

		err := jsonschema.Validate(falseSchema(), "anything")

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		assert.Empty(t, ve.Keyword, "a root false schema has no applicator context")
		assert.Equal(t, "value is not allowed", ve.Message)
	})
}

// TestPropertyNamesViolationIdentifiesKey pins the propertyNames error
// contract: the surfaced error carries Keyword "propertyNames" and an
// InstancePath pointing at the offending property's location, so a consumer
// can determine which key failed (and, by stripping the last segment, which
// object it belongs to) from stable fields rather than message parsing. The
// inner keyword failure (pattern, maxLength, and so on) stays in Causes.
func TestPropertyNamesViolationIdentifiesKey(t *testing.T) {
	t.Parallel()

	t.Run("pattern violation", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Type:          "object",
			PropertyNames: &jsonschema.Schema{Pattern: "^[a-z]+$"},
		}

		err := jsonschema.Validate(schema, map[string]any{"BadKey": 1.0, "good": 2.0})

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		assert.Equal(t, "propertyNames", ve.Keyword)
		assert.Equal(t, "/BadKey", ve.InstancePath,
			"the violation borrows the property's location")
		assert.Equal(t, "/propertyNames", ve.SchemaPath)
		assert.Contains(t, ve.Message, `"BadKey"`)

		require.Len(t, ve.Causes, 1)
		assert.Equal(t, "pattern", ve.Causes[0].Keyword,
			"the inner keyword failure stays available in Causes")
		assert.Equal(t, "/BadKey", ve.Causes[0].InstancePath)
	})

	t.Run("nested object", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"settings": {
					Type:          "object",
					PropertyNames: &jsonschema.Schema{MaxLength: jsonschema.Ptr(3)},
				},
			},
		}

		err := jsonschema.Validate(schema, map[string]any{
			"settings": map[string]any{"toolong": 1.0},
		})

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		assert.Equal(t, "propertyNames", ve.Keyword)
		assert.Equal(t, "/settings/toolong", ve.InstancePath,
			"the path identifies both the key and its containing object")
	})

	t.Run("key requiring pointer escaping", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Type:          "object",
			PropertyNames: &jsonschema.Schema{MaxLength: jsonschema.Ptr(1)},
		}

		err := jsonschema.Validate(schema, map[string]any{"a/b": 1.0})

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		assert.Equal(t, "/a~1b", ve.InstancePath,
			"the key is escaped per RFC 6901")
		assert.Contains(t, ve.Message, `"a/b"`)
	})

	t.Run("propertyNames false", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Type:          "object",
			PropertyNames: falseSchema(),
		}

		err := jsonschema.Validate(schema, map[string]any{"any": 1.0})

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		assert.Equal(t, "propertyNames", ve.Keyword)
		assert.Equal(t, "/any", ve.InstancePath)
	})
}

// TestValidationErrorStructure inspects the *ValidationError tree produced by
// specific failures rather than merely asserting that an error occurred. It
// pins the Keyword, InstancePath, SchemaPath, and Causes shape so a regression
// that rejected for the wrong reason (right error, wrong cause) would be caught.
func TestValidationErrorStructure(t *testing.T) {
	t.Parallel()

	t.Run("missing required property", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Type:     "object",
			Required: []string{"name"},
			Properties: map[string]*jsonschema.Schema{
				"name": {Type: "string"},
			},
		}

		err := jsonschema.Validate(schema, map[string]any{})

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		// A single failure surfaces as the leaf itself: the required keyword
		// points at the object root with no nested causes.
		assert.Equal(t, "required", ve.Keyword)
		assert.Empty(t, ve.InstancePath, "required failure is reported at the object root")
		assert.Equal(t, "/required", ve.SchemaPath)
		assert.Empty(t, ve.Causes, "a lone required failure is a childless leaf")
	})

	t.Run("maximum exceeded", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{Type: "number", Maximum: jsonschema.Ptr(10.0)}

		err := jsonschema.Validate(schema, 11.0)

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		assert.Equal(t, "maximum", ve.Keyword)
		assert.Equal(t, "/maximum", ve.SchemaPath)
		assert.Empty(t, ve.Causes)
	})

	t.Run("nested property carries instance path", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"address": {
					Type: "object",
					Properties: map[string]*jsonschema.Schema{
						"city": {Type: "string", MinLength: jsonschema.Ptr(3)},
					},
				},
			},
		}

		err := jsonschema.Validate(schema, map[string]any{
			"address": map[string]any{"city": "x"},
		})

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		// Container keywords flatten a single child failure into the parent, so
		// the deepest leaf rises to the root retaining its full nested paths.
		assert.Equal(t, "minLength", ve.Keyword)
		assert.Equal(t, "/address/city", ve.InstancePath,
			"nested failure carries the JSON Pointer to the failing value")
		assert.Equal(t, "/properties/address/properties/city/minLength", ve.SchemaPath)
	})

	t.Run("multiple property failures flatten into causes", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Type:     "object",
			Required: []string{"id"},
			Properties: map[string]*jsonschema.Schema{
				"id":   {Type: "string"},
				"meta": {Type: "object", Properties: map[string]*jsonschema.Schema{"n": {Type: "number"}}},
			},
		}

		err := jsonschema.Validate(schema, map[string]any{
			"meta": map[string]any{"n": "not-a-number"},
		})

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		// With more than one failure the root is an intermediate node whose
		// Causes hold the individual leaf errors, each keeping its own path.
		require.Len(t, ve.Causes, 2)
		assert.True(t, findErrorByKeyword(ve, "required"),
			"the missing top-level required property is reported")

		// Locate the nested type failure and assert its precise paths.
		var nested *jsonschema.ValidationError

		for _, cause := range ve.Causes {
			if cause.Keyword == "type" {
				nested = cause
			}
		}

		require.NotNil(t, nested, "nested type failure should be present among causes")
		assert.Equal(t, "/meta/n", nested.InstancePath)
		assert.Equal(t, "/properties/meta/properties/n/type", nested.SchemaPath)
	})

	t.Run("anyOf wraps branch failures", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			AnyOf: []*jsonschema.Schema{
				{Type: "string"},
				{Type: "boolean"},
			},
		}

		err := jsonschema.Validate(schema, 5.0)

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)

		// Compositional keywords wrap their child failures: the root is the
		// anyOf node and each branch's type failure is a cause beneath it.
		assert.Equal(t, "anyOf", ve.Keyword)
		assert.Equal(t, "/anyOf", ve.SchemaPath)
		require.Len(t, ve.Causes, 2)
		assert.Equal(t, "/anyOf/0/type", ve.Causes[0].SchemaPath)
		assert.Equal(t, "/anyOf/1/type", ve.Causes[1].SchemaPath)
	})
}

// TestDefaultDraftValidationRuns exercises the validator's behavior when
// $schema is absent. The suite runner's unmarshalTestSchema helper injects a
// $schema URI whenever a test omits one, so default-draft behavior is invisible
// through the suite. These assertions cover it directly: a schema with no
// $schema field still validates, confirming the default-draft path runs.
func TestDefaultDraftValidationRuns(t *testing.T) {
	t.Parallel()

	// No $schema: detectDraft falls through to Draft2020 (the zero value).
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"a": {Type: "integer"},
		},
	}

	tests := map[string]struct {
		instance any
		err      string
	}{
		"matching property validates": {
			instance: map[string]any{"a": float64(1)},
		},
		"wrong property type is rejected": {
			instance: map[string]any{"a": "not-an-integer"},
			err:      "(type)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

func TestContainsBasedErrorMatchingScope(t *testing.T) {
	t.Parallel()

	// 15 of ~18 table-driven test functions use Contains(err.Error(), ...)
	// With short substrings. The error tree is rendered recursively, so
	// a Contains check on the full string catches any keyword mention at
	// any depth. A test checking for "(type)" would pass even if the root
	// failure was actually "required" or "$ref".
	schema := &jsonschema.Schema{
		Type:     "object",
		Required: []string{"name"},
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
		},
	}

	// Missing required property produces a "required" error, but the
	// rendered error tree may also mention "type" in a different context.
	err := jsonschema.Validate(schema, map[string]any{})
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	found := findErrorByKeyword(ve, "required")
	assert.True(t, found,
		"should find a 'required' keyword error via structured matching")
}

// TestDefaultDraftIsDraft2020 proves the validator's default draft detection:
// when $schema is absent, detectDraft returns Draft2020 (the zero value), so an
// untagged schema behaves identically to one explicitly tagged 2020-12. The
// discriminator is $ref-sibling semantics: in 2020-12 keywords beside $ref are
// applied, in draft-07 they are ignored. A schema that $refs an integer
// definition and carries a sibling maximum:5 therefore rejects the integer 10
// under 2020-12 but accepts it under draft-07. The absent-$schema case must
// match the explicit-2020-12 case, not the draft-07 case.
func TestDefaultDraftIsDraft2020(t *testing.T) {
	t.Parallel()

	// The twentyTwenty helper builds a root that $refs #/$defs/x (an integer)
	// with a sibling maximum:5, using the supplied $schema value ("" means
	// absent).
	twentyTwenty := func(schemaURI string) *jsonschema.Schema {
		return &jsonschema.Schema{
			Schema:  schemaURI,
			Ref:     "#/$defs/x",
			Maximum: jsonschema.Ptr(5.0),
			Defs: map[string]*jsonschema.Schema{
				"x": {Type: "integer"},
			},
		}
	}
	// The draft7 schema is the equivalent under draft-07, using "definitions"
	// and the draft-07 ref prefix so the ref resolves under that draft.
	draft7 := &jsonschema.Schema{
		Schema:  "http://json-schema.org/draft-07/schema#",
		Ref:     "#/definitions/x",
		Maximum: jsonschema.Ptr(5.0),
		Definitions: map[string]*jsonschema.Schema{
			"x": {Type: "integer"},
		},
	}

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		// When siblingApplied is true the sibling maximum:5 is enforced, which
		// is the 2020-12 behavior.
		siblingApplied bool
	}{
		"absent $schema enforces $ref sibling (2020-12 default)": {
			schema:         twentyTwenty(""),
			instance:       float64(10),
			siblingApplied: true,
		},
		"explicit 2020-12 enforces $ref sibling": {
			schema:         twentyTwenty("https://json-schema.org/draft/2020-12/schema"),
			instance:       float64(10),
			siblingApplied: true,
		},
		"explicit draft-07 ignores $ref sibling": {
			schema:         draft7,
			instance:       float64(10),
			siblingApplied: false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.siblingApplied {
				require.Error(t, err, "sibling maximum should reject 10 under 2020-12")
				assert.Contains(t, err.Error(), "(maximum)")
			} else {
				require.NoError(t, err, "sibling maximum should be ignored under draft-07")
			}
		})
	}
}

// TestSubSchemaInheritsRootDraft proves sub-schemas inherit the root draft.
// Only the root carries $schema; a property sub-schema uses the same $ref-
// sibling discriminator. Under a draft-07 root the property's sibling maximum
// is ignored (inherited draft-07 semantics); under a 2020-12 root it is
// applied. To rule out the sibling being skipped because nothing was checked,
// each case also feeds a wrong-typed value so the $ref itself (type integer)
// rejects it, confirming the ref resolved through the inherited draft.
func TestSubSchemaInheritsRootDraft(t *testing.T) {
	t.Parallel()

	draft7Root := &jsonschema.Schema{
		Schema: "http://json-schema.org/draft-07/schema#",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"a": {Ref: "#/definitions/x", Maximum: jsonschema.Ptr(5.0)},
		},
		Definitions: map[string]*jsonschema.Schema{
			"x": {Type: "integer"},
		},
	}
	twentyTwentyRoot := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"a": {Ref: "#/$defs/x", Maximum: jsonschema.Ptr(5.0)},
		},
		Defs: map[string]*jsonschema.Schema{
			"x": {Type: "integer"},
		},
	}

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"draft-07 sub-schema ignores $ref sibling": {
			schema:   draft7Root,
			instance: map[string]any{"a": float64(10)},
			// maximum:5 ignored under inherited draft-07, so 10 is accepted.
		},
		"draft-07 sub-schema still resolves $ref": {
			schema:   draft7Root,
			instance: map[string]any{"a": "not-an-integer"},
			// The $ref (type integer) resolved and rejects the string, proving
			// the ref worked even though the sibling was ignored.
			err: "($ref)",
		},
		"2020-12 sub-schema applies $ref sibling": {
			schema:   twentyTwentyRoot,
			instance: map[string]any{"a": float64(10)},
			// maximum:5 applied under inherited 2020-12, so 10 is rejected.
			err: "(maximum)",
		},
		"2020-12 sub-schema also resolves $ref": {
			schema:   twentyTwentyRoot,
			instance: map[string]any{"a": "not-an-integer"},
			err:      "($ref)",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, tt.instance)
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}

// TestDraftSpecificSemanticsApplied proves the validator applies draft-specific
// semantics: the same logical schema and instance produce different outcomes per
// draft. The $ref-sibling rule is the cleanest discriminator the validator
// actually distinguishes (see validate.go: draft-07 returns early after $ref,
// 2020-12 continues to sibling keywords). A schema that $refs an integer with a
// sibling maximum:5 rejects the integer 10 under 2020-12 but accepts it under
// draft-07. If draft detection collapsed all schemas to one draft, both rows
// would share an outcome and this test would fail.
func TestDraftSpecificSemanticsApplied(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		// When rejected is true the integer 10 is rejected by the sibling
		// maximum:5, which only happens under 2020-12.
		rejected bool
	}{
		"2020-12 applies $ref sibling maximum": {
			schema: &jsonschema.Schema{
				Schema:  "https://json-schema.org/draft/2020-12/schema",
				Ref:     "#/$defs/x",
				Maximum: jsonschema.Ptr(5.0),
				Defs: map[string]*jsonschema.Schema{
					"x": {Type: "integer"},
				},
			},
			rejected: true,
		},
		"draft-07 ignores $ref sibling maximum": {
			schema: &jsonschema.Schema{
				Schema:  "http://json-schema.org/draft-07/schema#",
				Ref:     "#/definitions/x",
				Maximum: jsonschema.Ptr(5.0),
				Definitions: map[string]*jsonschema.Schema{
					"x": {Type: "integer"},
				},
			},
			rejected: false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tt.schema, float64(10))
			if tt.rejected {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "(maximum)")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestEmbeddedStructFieldShadowing is a schema-generation test (it uses
// GenerateFor) kept here because it guards validation-relevant schema
// correctness for multi-level struct embedding. A companion test in
// generate_test.go (TestFieldShadowingOuterWinsOverEmbedded)
// covers the type-distinction aspect.

func TestEmbeddedStructFieldShadowing(t *testing.T) {
	t.Parallel()

	// Across multiple levels of embedding, an outer field shadows an inner
	// promoted field of the same JSON name (grandchild promotion).

	type GrandChild struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	type Child struct {
		GrandChild
		Name string `json:"name"` // Shadows GrandChild.Name
	}

	type Parent struct {
		Child
	}

	s, err := jsonschema.GenerateFor[Parent]()
	require.NoError(t, err)

	// "name" should come from Child (which shadows GrandChild.Name).
	// "age" should be promoted from GrandChild.
	require.NotNil(t, s.Properties["name"],
		"shadowed field should be present")
	require.NotNil(t, s.Properties["age"],
		"grandchild field should be promoted")
}

// findValidationNode returns the first node in the error tree whose Keyword
// matches, or nil. Unlike findError it returns the node so callers can assert on
// SchemaPath and InstancePath together.
func findValidationNode(ve *jsonschema.ValidationError, keyword string) *jsonschema.ValidationError {
	if ve.Keyword == keyword {
		return ve
	}

	for _, cause := range ve.Causes {
		found := findValidationNode(cause, keyword)
		if found != nil {
			return found
		}
	}

	return nil
}

// collectLeaves returns every leaf node (no Causes) in the error tree.
func collectLeaves(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(ve.Causes) == 0 {
		return []*jsonschema.ValidationError{ve}
	}

	var leaves []*jsonschema.ValidationError

	for _, cause := range ve.Causes {
		leaves = append(leaves, collectLeaves(cause)...)
	}

	return leaves
}

// fixedResolver is a concurrency-safe RefResolver returning one fixed schema.
type fixedResolver struct{ schema *jsonschema.Schema }

func (r fixedResolver) ResolveRef(string) (*jsonschema.Schema, error) { return r.schema, nil }

func TestConcurrentValidationSharedSchema(t *testing.T) {
	t.Parallel()

	// Many goroutines validate against one shared *Schema. The schema exercises
	// the package-level regex cache (pattern/patternProperties) and the
	// remote-ref loader/registry paths. Run under -race.
	schema := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"code": {Type: "string", Pattern: "^[A-Z]{3}$"},
			"ref":  {Ref: "https://example.com/remote.json"},
		},
		PatternProperties: map[string]*jsonschema.Schema{
			"^x-": {Type: "string"},
		},
	}

	resolver := fixedResolver{schema: &jsonschema.Schema{Type: "string"}}

	const goroutines = 32

	var wg sync.WaitGroup

	for i := range goroutines {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			valid := map[string]any{"code": "ABC", "ref": "ok", "x-tag": "v"}
			err := jsonschema.Validate(schema, valid, jsonschema.WithRefResolver(resolver))
			if err != nil {
				t.Errorf("goroutine %d: valid instance rejected: %v", i, err)
			}

			invalid := map[string]any{"code": "abc", "ref": "ok"}
			err = jsonschema.Validate(schema, invalid, jsonschema.WithRefResolver(resolver))
			if err == nil {
				t.Errorf("goroutine %d: invalid code accepted", i)
			}
		}(i)
	}

	wg.Wait()
}

func TestResolveOptionsNotMutated(t *testing.T) {
	t.Parallel()

	// A shared *ResolveOptions is copied before a Loader is injected, so the
	// caller's value is never mutated across calls.
	opts := &jsonschema.ResolveOptions{}
	schema := &jsonschema.Schema{Type: "string"}

	for range 2 {
		require.NoError(t, jsonschema.Validate(schema, "x",
			jsonschema.WithResolveOptions(opts),
			jsonschema.WithRefResolver(fixedResolver{schema: &jsonschema.Schema{Type: "string"}}),
		))
	}

	assert.Nil(t, opts.Loader, "shared ResolveOptions.Loader must not be mutated by Validate")
}

func TestValidationErrorSchemaPath(t *testing.T) {
	t.Parallel()

	// Leaf errors carry an accurate SchemaPath into the schema tree.
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"address": {
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"city": {Type: "string", MinLength: jsonschema.Ptr(3)},
				},
			},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{
		"address": map[string]any{"city": "NY"},
	})
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	leaf := findValidationNode(ve, "minLength")
	require.NotNil(t, leaf, "expected a minLength error")
	assert.Equal(t, "/address/city", leaf.InstancePath)
	assert.Equal(t, "/properties/address/properties/city/minLength", leaf.SchemaPath)
}

func TestValidateCollectsAllErrors(t *testing.T) {
	t.Parallel()

	// Validation collects every failure rather than stopping at the first.
	schema := &jsonschema.Schema{
		Type:     "object",
		Required: []string{"name", "age", "tags"},
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
			"age":  {Type: "integer", Minimum: jsonschema.Ptr(0.0)},
			"tags": {Type: "array", MinItems: jsonschema.Ptr(1)},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{
		"name": 123.0,   // wrong type
		"age":  -1.0,    // below minimum
		"tags": []any{}, // too few items
	})
	require.Error(t, err)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	leaves := collectLeaves(ve)

	// Assert on the full multiset of (keyword, instancePath) pairs so a
	// regression that collapses or duplicates leaves is caught.
	got := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		got = append(got, leaf.Keyword+" "+leaf.InstancePath)
	}

	assert.ElementsMatch(t, []string{
		"type /name",
		"minimum /age",
		"minItems /tags",
	}, got, "all three failures must be collected simultaneously")
}

// TestValidateRefIntoUnknownKeyword covers $refs whose JSON Pointer targets a
// location with no typed Schema field: a sub-schema carried in an unknown
// keyword, or the internals of a non-applicator keyword such as examples.
// Upstream Schema.Resolve rejects these during pre-validation, but this package
// resolves $ref targets itself, so they resolve and the referenced constraint
// applies.
func TestValidateRefIntoUnknownKeyword(t *testing.T) {
	t.Parallel()

	const draft = `"https://json-schema.org/draft/2020-12/schema"`

	tests := map[string]struct {
		schema string
		data   string
		valid  bool
	}{
		"root unknown keyword, match": {
			schema: `{"$schema":` + draft + `,"unknown-keyword":{"type":"integer"},"properties":{"bar":{"$ref":"#/unknown-keyword"}}}`,
			data:   `{"bar":3}`,
			valid:  true,
		},
		"root unknown keyword, mismatch": {
			schema: `{"$schema":` + draft + `,"unknown-keyword":{"type":"integer"},"properties":{"bar":{"$ref":"#/unknown-keyword"}}}`,
			data:   `{"bar":true}`,
			valid:  false,
		},
		"encoded unknown keyword, match": {
			schema: `{"$schema":` + draft + `,"unknown/keyword":{"type":"integer"},"properties":{"bar":{"$ref":"#/unknown~1keyword"}}}`,
			data:   `{"bar":3}`,
			valid:  true,
		},
		"unknown keyword in sub-schema, match": {
			schema: `{"$schema":` + draft + `,"properties":{"foo":{"unknown-keyword":{"type":"integer"}},"bar":{"$ref":"#/properties/foo/unknown-keyword"}}}`,
			data:   `{"bar":3}`,
			valid:  true,
		},
		"unknown keyword in sub-schema, mismatch": {
			schema: `{"$schema":` + draft + `,"properties":{"foo":{"unknown-keyword":{"type":"integer"}},"bar":{"$ref":"#/properties/foo/unknown-keyword"}}}`,
			data:   `{"bar":true}`,
			valid:  false,
		},
		"internals of examples, match": {
			schema: `{"$schema":` + draft + `,"examples":[{"type":"string"}],"$ref":"#/examples/0"}`,
			data:   `"a string"`,
			valid:  true,
		},
		"internals of examples, mismatch": {
			schema: `{"$schema":` + draft + `,"examples":[{"type":"string"}],"$ref":"#/examples/0"}`,
			data:   `42`,
			valid:  false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var schema jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))

			err := jsonschema.ValidateJSON(&schema, []byte(tc.data))
			if tc.valid {
				assert.NoError(t, err, "expected valid")
			} else {
				assert.Error(t, err, "expected invalid")
			}
		})
	}
}

// TestValidateRefTargetWellFormed covers the structural validation of ref
// targets reached through untyped locations (unknown keywords, non-applicator
// internals). A malformed target — an uncompilable pattern or a broken nested
// ref — must keep the upstream pre-validation error fatal, while a target whose
// nested ref resolves against the root is accepted and its constraint applied.
func TestValidateRefTargetWellFormed(t *testing.T) {
	t.Parallel()

	const draft = `"https://json-schema.org/draft/2020-12/schema"`

	tests := map[string]struct {
		schema string
		data   string
		err    bool // true: a structural/resolve error must surface
		valid  bool // checked only when err is false
	}{
		"uncompilable pattern in ref-only target stays fatal": {
			schema: `{"$schema":` + draft + `,"unknown-keyword":{"type":"string","pattern":"("},"properties":{"bar":{"$ref":"#/unknown-keyword"}}}`,
			data:   `{"bar":"anything"}`,
			err:    true,
		},
		"broken nested ref in ref-only target stays fatal": {
			schema: `{"$schema":` + draft + `,"examples":[{"type":"string","$ref":"#/does/not/exist"}],"$ref":"#/examples/0"}`,
			data:   `"a string"`,
			err:    true,
		},
		"valid nested ref in ref-only target is applied": {
			schema: `{"$schema":` + draft + `,"$defs":{"anInt":{"type":"integer"}},"unknown-keyword":{"$ref":"#/$defs/anInt"},"properties":{"bar":{"$ref":"#/unknown-keyword"}}}`,
			data:   `{"bar":7}`,
			err:    false,
			valid:  true,
		},
		"valid nested ref in ref-only target rejects a violation": {
			schema: `{"$schema":` + draft + `,"$defs":{"anInt":{"type":"integer"}},"unknown-keyword":{"$ref":"#/$defs/anInt"},"properties":{"bar":{"$ref":"#/unknown-keyword"}}}`,
			data:   `{"bar":"not an int"}`,
			err:    false,
			valid:  false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var schema jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))

			err := jsonschema.ValidateJSON(&schema, []byte(tc.data))
			switch {
			case tc.err:
				require.Error(t, err, "a malformed ref-only target must keep the error fatal")
			case tc.valid:
				require.NoError(t, err, "expected valid")
			default:
				require.Error(t, err, "expected invalid")
			}
		})
	}
}

// aliasedSchema builds a schema whose two properties share one *Schema, so its
// sub-schema pointers do not form a tree.
func aliasedSchema() *jsonschema.Schema {
	shared := &jsonschema.Schema{Type: "string"}

	return &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared},
	}
}

// TestValidateResolveErrorStillFatal locks in the safety boundary of the
// ref-only Resolve-error exception: a schema that is genuinely malformed, or
// whose $ref cannot be resolved by this package either, must still surface the
// pre-validation error rather than silently passing.
func TestValidateResolveErrorStillFatal(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
	}{
		"typo in local ref does not resolve": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{
					"name": {Ref: "#/$defs/Usre"}, // typo: should be "User"
				},
				Defs: map[string]*jsonschema.Schema{
					"User": {Type: "string"},
				},
			},
			instance: map[string]any{"name": 42.0},
		},
		"index into single-schema items does not resolve": {
			schema: &jsonschema.Schema{
				Schema: "https://json-schema.org/draft/2020-12/schema",
				Items:  &jsonschema.Schema{Type: "string"},
				Defs: map[string]*jsonschema.Schema{
					"check": {Ref: "#/items/0"},
				},
			},
			instance: []any{"hello"},
		},
		// Aliased sub-schema pointers do not form a tree; upstream rejects this.
		// The ref-only exception must not mask it, even with a valid instance
		// and no refs in play.
		"aliased sub-schema pointers are not a tree": {
			schema:   aliasedSchema(),
			instance: map[string]any{"a": "x", "b": "y"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(tc.schema, tc.instance)
			require.Error(t, err,
				"an unresolvable ref must not be reclassified as a tolerable ref-only Resolve error")
		})
	}
}
