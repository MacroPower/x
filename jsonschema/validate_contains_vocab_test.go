package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateContainsFloorWithoutValidationVocab locks in the vocabulary split
// of the contains cluster: the at-least-one rule with its default minContains=1
// floor belongs to contains itself (2020-12 core section 10.3.1.3, applicator
// vocabulary), while only the explicit minContains/maxContains bounds belong to
// the validation vocabulary. Disabling the validation vocabulary therefore
// skips the explicit bounds but keeps the default floor.
func TestValidateContainsFloorWithoutValidationVocab(t *testing.T) {
	t.Parallel()

	applicatorOnly := jsonschema.WithVocabularies(
		jsonschema.VocabCore2020,
		jsonschema.VocabApplicator2020,
	)

	// The subschemas are the boolean true/false forms: an assertion keyword
	// such as type inside the contains subschema would itself be gated on the
	// disabled validation vocabulary and match vacuously, while the boolean
	// forms accept and reject independent of any vocabulary.
	tests := map[string]struct {
		schema   *jsonschema.Schema
		opts     []jsonschema.ValidateOption
		instance string
		valid    bool
		keyword  string
	}{
		"default floor rejects an empty array without the validation vocabulary": {
			schema:   &jsonschema.Schema{Contains: &jsonschema.Schema{}},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[]`,
			keyword:  jsonschema.KeywordContains,
		},
		"default floor rejects when no element matches": {
			schema:   &jsonschema.Schema{Contains: &jsonschema.Schema{Not: &jsonschema.Schema{}}},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[1, 2]`,
			keyword:  jsonschema.KeywordContains,
		},
		"a matching element satisfies the floor without the validation vocabulary": {
			schema:   &jsonschema.Schema{Contains: &jsonschema.Schema{}},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[1]`,
			valid:    true,
		},
		"a skipped minContains cannot lower the default floor": {
			schema: &jsonschema.Schema{
				Contains:    &jsonschema.Schema{},
				MinContains: new(0),
			},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[]`,
			keyword:  jsonschema.KeywordContains,
		},
		"a skipped minContains cannot raise the default floor": {
			schema: &jsonschema.Schema{
				Contains:    &jsonschema.Schema{},
				MinContains: new(3),
			},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[1]`,
			valid:    true,
		},
		"explicit maxContains is skipped without the validation vocabulary": {
			schema: &jsonschema.Schema{
				Contains:    &jsonschema.Schema{},
				MaxContains: new(1),
			},
			opts:     []jsonschema.ValidateOption{applicatorOnly},
			instance: `[1, 2]`,
			valid:    true,
		},
		"explicit minContains applies with the full vocabulary set": {
			schema: &jsonschema.Schema{
				Contains:    &jsonschema.Schema{},
				MinContains: new(0),
			},
			instance: `[]`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.Compile(t.Context(), tc.schema, tc.opts...)
			require.NoError(t, err)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword,
				"a default-floor shortfall is a contains failure, never a skipped minContains violation")
		})
	}
}
