package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateDraft7EmptyItemsArray locks in Draft-07 array-form semantics for
// a present-but-empty items array (JSON "items": [], which parses to a non-nil
// empty ItemsArray): additionalItems applies to every index at or beyond the
// tuple length, which is every index when the tuple is empty. Conflating the
// empty tuple with an absent items keyword would silently drop additionalItems
// and accept every array.
func TestValidateDraft7EmptyItemsArray(t *testing.T) {
	t.Parallel()

	const draft7 = `"http://json-schema.org/draft-07/schema#"`

	tests := map[string]struct {
		schema   string
		instance string
		valid    bool
		keyword  string
	}{
		"false additionalItems rejects every element": {
			schema:   `{"$schema": ` + draft7 + `, "items": [], "additionalItems": false}`,
			instance: `[1]`,
			keyword:  jsonschema.KeywordAdditionalItems,
		},
		"false additionalItems accepts an empty array": {
			schema:   `{"$schema": ` + draft7 + `, "items": [], "additionalItems": false}`,
			instance: `[]`,
			valid:    true,
		},
		"schema additionalItems applies from index zero": {
			schema:   `{"$schema": ` + draft7 + `, "items": [], "additionalItems": {"type": "string"}}`,
			instance: `[1]`,
			keyword:  jsonschema.KeywordType,
		},
		"schema additionalItems accepts conforming elements": {
			schema:   `{"$schema": ` + draft7 + `, "items": [], "additionalItems": {"type": "string"}}`,
			instance: `["a", "b"]`,
			valid:    true,
		},
		"empty items array alone constrains nothing": {
			schema:   `{"$schema": ` + draft7 + `, "items": []}`,
			instance: `[1, "a"]`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err,
				"additionalItems governs every index beyond the empty tuple")

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword)
		})
	}
}
