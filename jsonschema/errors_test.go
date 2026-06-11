package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestValidationError_Leaves(t *testing.T) {
	t.Parallel()

	leaf := func(path, keyword string) *jsonschema.ValidationError {
		return &jsonschema.ValidationError{InstancePath: path, Keyword: keyword}
	}

	typeName := leaf("/name", "type")
	typeAge := leaf("/age", "type")

	// The propertyNames node is a leaf despite carrying the inner name-check.
	propNames := &jsonschema.ValidationError{
		InstancePath: "/BadKey",
		Keyword:      "propertyNames",
		Causes:       []*jsonschema.ValidationError{leaf("/BadKey", "pattern")},
	}

	tcs := map[string]struct {
		err  *jsonschema.ValidationError
		want []*jsonschema.ValidationError
	}{
		"single leaf returns itself": {
			err:  typeName,
			want: []*jsonschema.ValidationError{typeName},
		},
		"synthetic root flattens to its leaves": {
			err:  &jsonschema.ValidationError{Causes: []*jsonschema.ValidationError{typeName, typeAge}},
			want: []*jsonschema.ValidationError{typeName, typeAge},
		},
		"nested wrappers are descended": {
			err: &jsonschema.ValidationError{
				Keyword: "anyOf",
				Message: "did not validate against any subschema",
				Causes: []*jsonschema.ValidationError{
					{Keyword: "allOf", Causes: []*jsonschema.ValidationError{typeName}},
					typeAge,
				},
			},
			want: []*jsonschema.ValidationError{typeName, typeAge},
		},
		"propertyNames is a leaf, its cause is not descended": {
			err:  propNames,
			want: []*jsonschema.ValidationError{propNames},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.err.Leaves())
		})
	}
}

func TestValidationError_Leaves_SharedNodeReturnedOnce(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.ValidationError{InstancePath: "/x", Keyword: "type"}
	root := &jsonschema.ValidationError{
		Causes: []*jsonschema.ValidationError{
			{Keyword: "anyOf", Causes: []*jsonschema.ValidationError{shared}},
			{Keyword: "oneOf", Causes: []*jsonschema.ValidationError{shared}},
		},
	}

	assert.Equal(t, []*jsonschema.ValidationError{shared}, root.Leaves())
}

func TestValidationError_Leaves_EndToEnd(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
			"age":  {Type: "number"},
		},
	}

	err := jsonschema.Validate(schema, map[string]any{"name": 1, "age": "x"})

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	leaves := ve.Leaves()
	require.Len(t, leaves, 2)

	paths := []string{leaves[0].InstancePath, leaves[1].InstancePath}
	assert.ElementsMatch(t, []string{"/name", "/age"}, paths)

	for _, l := range leaves {
		assert.Equal(t, "type", l.Keyword)
	}
}

func TestValidationError_TargetsKey(t *testing.T) {
	t.Parallel()

	key := []string{
		"additionalProperties", "propertyNames", "required",
		"minProperties", "maxProperties",
		"minItems", "maxItems", "uniqueItems",
		"contains", "minContains", "maxContains",
	}
	value := []string{
		"type", "enum", "const", "pattern", "minimum", "maximum",
		"minLength", "maxLength", "format", "multipleOf", "",
	}

	for _, keyword := range key {
		t.Run("key/"+keyword, func(t *testing.T) {
			t.Parallel()

			assert.True(t, (&jsonschema.ValidationError{Keyword: keyword}).TargetsKey())
		})
	}

	for _, keyword := range value {
		t.Run("value/"+keyword, func(t *testing.T) {
			t.Parallel()

			assert.False(t, (&jsonschema.ValidationError{Keyword: keyword}).TargetsKey())
		})
	}
}
