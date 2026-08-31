package jsonschema_test

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateDependenciesFalseSchemaKeyword locks in the error contract for a
// boolean-false dependency subschema: like every other applicator call site,
// the dependency keywords stamp their keyword on the false-schema leaf, so a
// consumer branching on ValidationError.Keyword sees dependentSchemas (or the
// legacy dependencies) rather than an empty keyword.
func TestValidateDependenciesFalseSchemaKeyword(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema     string
		keyword    string
		schemaPath jsontext.Pointer
	}{
		"dependentSchemas false subschema": {
			schema:     `{"dependentSchemas": {"a": false}}`,
			keyword:    jsonschema.KeywordDependentSchemas,
			schemaPath: "/dependentSchemas/a",
		},
		"legacy dependencies false subschema": {
			schema:     `{"$schema": "http://json-schema.org/draft-07/schema#", "dependencies": {"a": false}}`,
			keyword:    jsonschema.KeywordDependencies,
			schemaPath: "/dependencies/a",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err)

			err = v.Validate(t.Context(), map[string]any{"a": 1.0})
			require.Error(t, err)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword,
				"the false-schema leaf must carry the applying dependency keyword")
			assert.Equal(t, tc.schemaPath, ve.SchemaPath)
			assert.Equal(t, "value is not allowed", ve.Message)

			require.NoError(t, v.Validate(t.Context(), map[string]any{"b": 1.0}),
				"an absent trigger property leaves the dependency inert")
		})
	}
}
