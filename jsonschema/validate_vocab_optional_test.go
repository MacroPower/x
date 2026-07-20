package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// A metaschema may declare a vocabulary with false, marking it optional for
// implementations that do not recognize it (core section 8.1.2). This
// implementation recognizes every standard 2020-12 vocabulary, so the value
// has no impact here: a recognized vocabulary's keywords apply whenever its
// URI is listed. Only omitting the URI deactivates the group.
func TestValidateVocabularyDeclaredOptional(t *testing.T) {
	t.Parallel()

	metaFor := func(vocabs map[string]bool) jsonschema.ValidateOption {
		return jsonschema.WithMetaSchemaResolver(jsonschema.SchemaMap{
			"https://example.com/my-meta": {
				ID:         "https://example.com/my-meta",
				Vocabulary: vocabs,
			},
		})
	}

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		vocabs   map[string]bool
		err      string
	}{
		"applicator declared false still applies properties": {
			schema: &jsonschema.Schema{
				Schema:     "https://example.com/my-meta",
				Properties: map[string]*jsonschema.Schema{"a": {Type: "string"}},
			},
			instance: map[string]any{"a": 5.0},
			vocabs: map[string]bool{
				jsonschema.VocabCore2020:       true,
				jsonschema.VocabApplicator2020: false,
				jsonschema.VocabValidation2020: true,
			},
			err: "(type)",
		},
		"validation declared false still asserts type": {
			schema: &jsonschema.Schema{
				Schema: "https://example.com/my-meta",
				Type:   "string",
			},
			instance: 42.0,
			vocabs: map[string]bool{
				jsonschema.VocabCore2020:       true,
				jsonschema.VocabValidation2020: false,
			},
			err: "(type)",
		},
		"format-assertion declared false still asserts format": {
			schema: &jsonschema.Schema{
				Schema: "https://example.com/my-meta",
				Format: "ipv4",
			},
			instance: "not-an-ip",
			vocabs: map[string]bool{
				jsonschema.VocabCore2020:            true,
				jsonschema.VocabFormatAssertion2020: false,
			},
			err: "(format)",
		},
		"omitted validation vocabulary stays inactive": {
			schema: &jsonschema.Schema{
				Schema: "https://example.com/my-meta",
				Type:   "string",
			},
			instance: 42.0,
			vocabs: map[string]bool{
				jsonschema.VocabCore2020:       true,
				jsonschema.VocabApplicator2020: false,
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), tt.schema, tt.instance, metaFor(tt.vocabs))
			if tt.err == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.err)
			}
		})
	}
}
