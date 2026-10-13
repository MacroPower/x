package jsonschema_test

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// deprecationRecords returns each record in ds as its instance path and
// its schema path, with ", key" after a record that marks a key.
func deprecationRecords(ds []jsonschema.Deprecation) []string {
	var out []string

	for _, d := range ds {
		rec := fmt.Sprintf("%q at %q", d.InstancePath, d.SchemaPath)
		if d.TargetsKey() {
			rec += ", key"
		}

		out = append(out, rec)
	}

	return out
}

func TestValidatorValidateDeprecations(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   string
		instance string
		opts     []jsonschema.ValidateOption
		want     []string
		invalid  bool
	}{
		"root schema": {
			schema:   `{"deprecated": true}`,
			instance: `{}`,
			want:     []string{`"" at ""`},
		},
		"no deprecated keyword": {
			schema:   `{"properties": {"a": {"type": "integer"}}}`,
			instance: `{"a": 1}`,
		},
		"property marks the key": {
			schema:   `{"properties": {"a": {"deprecated": true}}}`,
			instance: `{"a": 1, "b": 2}`,
			want:     []string{`"/a" at "/properties/a", key`},
		},
		"absent property records nothing": {
			schema:   `{"properties": {"a": {"deprecated": true}}}`,
			instance: `{"b": 2}`,
		},
		"pattern property marks the key": {
			schema:   `{"patternProperties": {"^x-": {"deprecated": true}}}`,
			instance: `{"x-a": 1, "b": 2}`,
			want:     []string{`"/x-a" at "/patternProperties/^x-", key`},
		},
		"additional property marks the key": {
			schema:   `{"properties": {"a": {}}, "additionalProperties": {"deprecated": true}}`,
			instance: `{"a": 1, "b": 2}`,
			want:     []string{`"/b" at "/additionalProperties", key`},
		},
		"unevaluated property with no assertion marks the key": {
			schema:   `{"properties": {"a": {}}, "unevaluatedProperties": {"deprecated": true}}`,
			instance: `{"a": 1, "b": 2, "c": 3}`,
			want: []string{
				`"/b" at "/unevaluatedProperties", key`,
				`"/c" at "/unevaluatedProperties", key`,
			},
		},
		"property name marks the key": {
			schema:   `{"propertyNames": {"anyOf": [{"const": "old", "deprecated": true}, {}]}}`,
			instance: `{"new": 1, "old": 2}`,
			want:     []string{`"/old" at "/propertyNames/anyOf/0", key`},
		},
		"schema behind a ref marks the value": {
			schema:   `{"properties": {"a": {"$ref": "#/$defs/A"}}, "$defs": {"A": {"deprecated": true}}}`,
			instance: `{"a": 1}`,
			want:     []string{`"/a" at "/properties/a/$ref"`},
		},
		"ref sibling under 2020-12": {
			schema:   `{"properties": {"a": {"$ref": "#/$defs/A", "deprecated": true}}, "$defs": {"A": {}}}`,
			instance: `{"a": 1}`,
			want:     []string{`"/a" at "/properties/a", key`},
		},
		"ref sibling under draft-07 records nothing": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {"a": {"$ref": "#/definitions/A", "deprecated": true}},
					"definitions": {"A": {}}
				}
			`),
			instance: `{"a": 1}`,
		},
		"ref in allOf under draft-07": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {"a": {"allOf": [{"$ref": "#/definitions/A"}], "deprecated": true}},
					"definitions": {"A": {}}
				}
			`),
			instance: `{"a": 1}`,
			want:     []string{`"/a" at "/properties/a", key`},
		},
		"items mark each value": {
			schema:   `{"items": {"deprecated": true}}`,
			instance: `[1, 2]`,
			want: []string{
				`"/0" at "/items"`,
				`"/1" at "/items"`,
			},
		},
		"prefix items mark the values they reach": {
			schema:   `{"prefixItems": [{}, {"deprecated": true}, {"deprecated": true}]}`,
			instance: `[1, 2]`,
			want:     []string{`"/1" at "/prefixItems/1"`},
		},
		"unevaluated items with no assertion": {
			schema:   `{"prefixItems": [{}], "unevaluatedItems": {"deprecated": true}}`,
			instance: `[1, 2]`,
			want:     []string{`"/1" at "/unevaluatedItems"`},
		},
		"anyOf keeps a branch the value passes": {
			schema:   `{"anyOf": [{"type": "string", "deprecated": true}, {"type": "integer", "deprecated": true}]}`,
			instance: `"wep"`,
			want:     []string{`"" at "/anyOf/0"`},
		},
		"anyOf drops a branch the value fails": {
			schema:   `{"anyOf": [{"type": "string", "deprecated": true}, {"type": "integer"}]}`,
			instance: `1`,
		},
		"oneOf drops a branch the value fails": {
			schema:   `{"oneOf": [{"type": "string", "deprecated": true}, {"type": "integer", "deprecated": true}]}`,
			instance: `1`,
			want:     []string{`"" at "/oneOf/1"`},
		},
		"oneOf keeps each branch that passes when several pass": {
			schema:   `{"oneOf": [{"deprecated": true}, {"type": "string"}, {"deprecated": true}]}`,
			instance: `1`,
			want: []string{
				`"" at "/oneOf/0"`,
				`"" at "/oneOf/2"`,
			},
			invalid: true,
		},
		"if that passes keeps its records and then's": {
			schema: stringtest.Input(`
				{
					"if": {"const": "wep", "deprecated": true},
					"then": {"maxLength": 1, "deprecated": true},
					"else": {"deprecated": true}
				}
			`),
			instance: `"wep"`,
			want: []string{
				`"" at "/if"`,
				`"" at "/then"`,
			},
			invalid: true,
		},
		"if that fails drops its records and keeps else's": {
			schema: stringtest.Input(`
				{
					"if": {"const": "wep", "deprecated": true},
					"then": {"deprecated": true},
					"else": {"deprecated": true}
				}
			`),
			instance: `"wpa3"`,
			want:     []string{`"" at "/else"`},
		},
		"contains keeps the items that match": {
			schema:   `{"contains": {"const": 1, "deprecated": true}}`,
			instance: `[1, 2, 1]`,
			want: []string{
				`"/0" at "/contains"`,
				`"/2" at "/contains"`,
			},
		},
		"not records nothing": {
			schema:   `{"not": {"type": "string", "deprecated": true}}`,
			instance: `1`,
		},
		"failing allOf keeps its records": {
			schema:   `{"allOf": [{"type": "string", "deprecated": true}]}`,
			instance: `1`,
			want:     []string{`"" at "/allOf/0"`},
			invalid:  true,
		},
		"failing ref keeps its records": {
			schema:   `{"$ref": "#/$defs/A", "$defs": {"A": {"type": "string", "deprecated": true}}}`,
			instance: `1`,
			want:     []string{`"" at "/$ref"`},
			invalid:  true,
		},
		"dependent schema": {
			schema:   `{"dependentSchemas": {"a": {"deprecated": true}}}`,
			instance: `{"a": 1}`,
			want:     []string{`"" at "/dependentSchemas/a"`},
		},
		"legacy dependencies schema": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"dependencies": {"a": {"deprecated": true}}
				}
			`),
			instance: `{"a": 1}`,
			want:     []string{`"" at "/dependencies/a"`},
		},
		"two paths to one value give two records": {
			schema:   `{"properties": {"a": {"deprecated": true}}, "allOf": [{"properties": {"a": {"deprecated": true}}}]}`,
			instance: `{"a": 1}`,
			want: []string{
				`"/a" at "/properties/a", key`,
				`"/a" at "/allOf/0/properties/a", key`,
			},
		},
		"cycle the walk cuts records nothing": {
			schema:   `{"deprecated": true, "anyOf": [{"$ref": "#"}]}`,
			instance: `1`,
			want:     []string{`"" at ""`},
		},
		"recursive ref records each level": {
			schema: stringtest.Input(`
				{
					"$ref": "#/$defs/node",
					"$defs": {"node": {"deprecated": true, "properties": {"next": {"$ref": "#/$defs/node"}}}}
				}
			`),
			instance: `{"next": {}}`,
			want: []string{
				`"" at "/$ref"`,
				`"/next" at "/$ref/properties/next/$ref"`,
			},
		},
		"core vocabulary alone still reports the keyword": {
			schema:   `{"deprecated": true}`,
			instance: `1`,
			opts:     []jsonschema.ValidateOption{jsonschema.WithVocabularies(jsonschema.VocabCore2020)},
			want:     []string{`"" at ""`},
		},
		"failing instance keeps the records": {
			schema:   `{"properties": {"a": {"type": "string", "deprecated": true}}}`,
			instance: `{"a": 1}`,
			want:     []string{`"/a" at "/properties/a", key`},
			invalid:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema), tc.opts...)
			require.NoError(t, err)

			var instance any

			require.NoError(t, json.Unmarshal([]byte(tc.instance), &instance))

			got, err := v.ValidateDeprecations(t.Context(), instance)
			assert.Equal(t, tc.want, deprecationRecords(got))

			want := v.Validate(t.Context(), instance)
			if tc.invalid {
				require.Error(t, want)
				require.EqualError(t, err, want.Error())

				_, ok := errors.AsType[*jsonschema.ValidationError](err)
				assert.True(t, ok)
			} else {
				require.NoError(t, want)
				require.NoError(t, err)
			}

			for _, d := range got {
				assert.True(t, d.Schema.Deprecated, "the record names the schema that holds the keyword")
			}
		})
	}
}

// TestValidatorValidateDeprecationsSchema checks that a record names the
// schema that holds the keyword, the one a $ref reaches, so a caller reads
// its deprecationMessage, and that the segments match the pointers.
func TestValidatorValidateDeprecationsSchema(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.CompileJSON(t.Context(), []byte(stringtest.Input(`
		{
			"properties": {"wifi": {"items": {"$ref": "#/$defs/WiFi"}}},
			"$defs": {"WiFi": {"deprecated": true, "deprecationMessage": "set wifi.mode"}}
		}
	`)))
	require.NoError(t, err)

	got, err := v.ValidateDeprecations(t.Context(), map[string]any{"wifi": []any{"wep"}})
	require.NoError(t, err)
	require.Len(t, got, 1)

	d := got[0]
	assert.Equal(t, "set wifi.mode", d.Schema.Extra[jsonschema.KeywordDeprecationMessage])
	assert.Equal(t, []jsonschema.Segment{{Key: "wifi"}, {Index: 0, IsIndex: true}}, d.InstanceSegments())
	assert.Equal(t, []jsonschema.Segment{
		{Key: "properties"}, {Key: "wifi"}, {Key: "items"}, {Key: "$ref"},
	}, d.SchemaSegments())
	assert.False(t, d.TargetsKey())
}

// TestValidatorValidateDeprecationsRejectsInstance checks that an instance
// Validate does not accept returns its error and no record.
func TestValidatorValidateDeprecationsRejectsInstance(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.CompileJSON(t.Context(), []byte(`{"deprecated": true}`))
	require.NoError(t, err)

	got, err := v.ValidateDeprecations(t.Context(), struct{}{})
	require.Error(t, err)
	assert.Nil(t, got)
}
