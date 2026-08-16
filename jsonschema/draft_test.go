package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestWithDraft_ValidationOverride covers WithDraft as a ValidateOption: it
// overrides the draft otherwise detected from the root schema's $schema.
// Format assertion makes the draft observable: Draft-07 asserts format by
// default, while Draft 2020-12 treats it as annotation-only.
func TestWithDraft_ValidationOverride(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schemaURI string
		opts      []jsonschema.ValidateOption
		valid     bool
	}{
		"no $schema defaults to 2020 and annotates": {
			valid: true,
		},
		"no $schema with Draft7 override asserts": {
			opts:  []jsonschema.ValidateOption{jsonschema.WithDraft(jsonschema.Draft7)},
			valid: false,
		},
		"draft-07 $schema asserts": {
			schemaURI: "http://json-schema.org/draft-07/schema#",
			valid:     false,
		},
		"Draft2020 override beats draft-07 $schema": {
			schemaURI: "http://json-schema.org/draft-07/schema#",
			opts:      []jsonschema.ValidateOption{jsonschema.WithDraft(jsonschema.Draft2020)},
			valid:     true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{Schema: tc.schemaURI, Format: "email"}

			err := jsonschema.Validate(t.Context(), schema, "definitely not an email", tc.opts...)
			if tc.valid {
				assert.NoError(t, err)

				return
			}

			var verr *jsonschema.ValidationError

			require.ErrorAs(t, err, &verr)
			assert.Equal(t, "format", verr.Keyword)
		})
	}
}

// TestWithDraft_InlineOverride covers WithDraft as an InlineOption. The $ref
// sibling semantics make the draft observable: Draft 2020-12 keeps siblings
// and joins the target into allOf, while Draft-07 ignores siblings and
// replaces the node with the target copy alone.
func TestWithDraft_InlineOverride(t *testing.T) {
	t.Parallel()

	newSchema := func() *jsonschema.Schema {
		return &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{
				"a": {Ref: "#/$defs/t", Description: "annotated"},
			},
			Defs: map[string]*jsonschema.Schema{
				"t": {Type: "string"},
			},
		}
	}

	t.Run("no $schema defaults to 2020 sibling handling", func(t *testing.T) {
		t.Parallel()

		got, err := jsonschema.Inline(t.Context(), newSchema())
		require.NoError(t, err)

		prop := got.Properties["a"]
		require.NotNil(t, prop)
		assert.Empty(t, prop.Ref)
		assert.Equal(t, "annotated", prop.Description)
		require.Len(t, prop.AllOf, 1)
		assert.Equal(t, "string", prop.AllOf[0].Type)
	})

	t.Run("Draft7 override drops siblings", func(t *testing.T) {
		t.Parallel()

		got, err := jsonschema.Inline(t.Context(), newSchema(), jsonschema.WithDraft(jsonschema.Draft7))
		require.NoError(t, err)

		prop := got.Properties["a"]
		require.NotNil(t, prop)
		assert.Empty(t, prop.Ref)
		assert.Empty(t, prop.Description)
		assert.Empty(t, prop.AllOf)
		assert.Equal(t, "string", prop.Type)
	})
}

// TestWithDraft_GenerateUnchanged pins the original GenerateOption role: the
// same option value still selects the generation target draft.
func TestWithDraft_GenerateUnchanged(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[string](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
	require.NoError(t, err)

	assert.Equal(t, "http://json-schema.org/draft-07/schema#", s.Schema)
}

// TestUnsupportedDeclaredDialect pins that a root schema declaring an official
// dialect this package does not implement fails Compile and Inline with
// ErrUnsupportedDraft instead of being silently processed as 2020-12 (under
// which, for example, a 2019-09 $recursiveRef lands in Extra and asserts
// nothing). A WithDraft override processes the document explicitly, and a
// non-official custom metaschema URI keeps the 2020-12 default.
func TestUnsupportedDeclaredDialect(t *testing.T) {
	t.Parallel()

	for name, uri := range map[string]string{
		"2019-09":                "https://json-schema.org/draft/2019-09/schema",
		"2019-09 with fragment":  "https://json-schema.org/draft/2019-09/schema#",
		"draft-06":               "http://json-schema.org/draft-06/schema#",
		"draft-04":               "http://json-schema.org/draft-04/schema#",
		"draft-03":               "http://json-schema.org/draft-03/schema#",
		"draft-04 https no frag": "https://json-schema.org/draft-04/schema",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{Schema: uri, Type: "object"}

			_, err := jsonschema.Compile(t.Context(), schema)
			require.ErrorIs(t, err, jsonschema.ErrUnsupportedDraft)

			_, err = jsonschema.Inline(t.Context(), schema)
			require.ErrorIs(t, err, jsonschema.ErrUnsupportedDraft)

			// The override is the explicit escape hatch.
			_, err = jsonschema.Compile(t.Context(), schema,
				jsonschema.WithDraft(jsonschema.Draft2020))
			require.NoError(t, err)
		})
	}

	t.Run("custom metaschema URI keeps the default", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Schema: "https://example.com/my-metaschema",
			Type:   "object",
		}

		_, err := jsonschema.Compile(t.Context(), schema)
		require.NoError(t, err)
	})
}
