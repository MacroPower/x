package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateUnknownFormatUnderFormatAssertionVocabulary locks in the 2020-12
// requirement (validation section 7.2.3) that an implementation fails on
// unknown formats when the format-assertion vocabulary is specified: with
// assertion driven by that vocabulary, a format name with no registered
// checker rejects every string instance. The WithFormats(true) opt-in and
// Draft-07's default assertion are the package's own contracts and stay
// lenient for unknown names.
func TestValidateUnknownFormatUnderFormatAssertionVocabulary(t *testing.T) {
	t.Parallel()

	assertionVocabs := jsonschema.WithVocabularies(
		jsonschema.VocabCore2020,
		jsonschema.VocabValidation2020,
		jsonschema.VocabApplicator2020,
		jsonschema.VocabFormatAssertion2020,
	)

	tests := map[string]struct {
		schema   *jsonschema.Schema
		opts     []jsonschema.ValidateOption
		instance any
		valid    bool
		keyword  string
	}{
		"unknown format fails under the vocabulary": {
			schema:   &jsonschema.Schema{Format: "no-such-format"},
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: "x",
			keyword:  jsonschema.KeywordFormat,
		},
		"known format still asserted under the vocabulary": {
			schema:   &jsonschema.Schema{Format: "ipv4"},
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: "not-an-ip",
			keyword:  jsonschema.KeywordFormat,
		},
		"known format accepts a conforming instance under the vocabulary": {
			schema:   &jsonschema.Schema{Format: "ipv4"},
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: "192.168.0.1",
			valid:    true,
		},
		"unknown format ignores non-string instances": {
			schema:   &jsonschema.Schema{Format: "no-such-format"},
			opts:     []jsonschema.ValidateOption{assertionVocabs},
			instance: 42.0,
			valid:    true,
		},
		"unknown format stays lenient under WithFormats force": {
			schema:   &jsonschema.Schema{Format: "no-such-format"},
			opts:     []jsonschema.ValidateOption{jsonschema.WithFormats(true)},
			instance: "x",
			valid:    true,
		},
		"WithFormats force overrides the vocabulary-driven strictness": {
			schema:   &jsonschema.Schema{Format: "no-such-format"},
			opts:     []jsonschema.ValidateOption{assertionVocabs, jsonschema.WithFormats(true)},
			instance: "x",
			valid:    true,
		},
		"unknown format stays lenient under draft-07 default assertion": {
			schema: &jsonschema.Schema{
				Schema: "http://json-schema.org/draft-07/schema#",
				Format: "no-such-format",
			},
			instance: "x",
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), tc.schema, tc.instance, tc.opts...)
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tc.keyword, ve.Keyword)
		})
	}
}
