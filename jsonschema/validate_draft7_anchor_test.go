package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestDraft7AnchorKeywordsRegisterNothing pins that $anchor and $dynamicAnchor
// are Draft 2020-12 keywords in the shared ref-resolution registry: under
// Draft-07 they are unknown annotations that register no resolution targets, so
// a plain-name fragment $ref naming one stays unresolvable instead of asserting
// the target, matching what a conforming Draft-07 processor produces. The
// Draft-07 spelling of an anchor (a fragment-only $id) and the 2020-12 $anchor
// keyword under its own draft both still resolve.
func TestDraft7AnchorKeywordsRegisterNothing(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema  string
		compile bool // whether Compile succeeds
		valid   bool // for a compiled schema: whether "not an int" passes
	}{
		"draft-07 $anchor is inert": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"allOf": [{"$ref": "#a"}],
					"definitions": {"x": {"$anchor": "a", "type": "integer"}}
				}
			`),
			compile: false,
		},
		"draft-07 $dynamicAnchor is inert": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"allOf": [{"$ref": "#a"}],
					"definitions": {"x": {"$dynamicAnchor": "a", "type": "integer"}}
				}
			`),
			compile: false,
		},
		"draft-07 fragment-only $id still names an anchor": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"allOf": [{"$ref": "#a"}],
					"definitions": {"x": {"$id": "#a", "type": "integer"}}
				}
			`),
			compile: true,
			valid:   false,
		},
		"draft 2020-12 $anchor still resolves": {
			schema: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"allOf": [{"$ref": "#a"}],
					"$defs": {"x": {"$anchor": "a", "type": "integer"}}
				}
			`),
			compile: true,
			valid:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), schema)
			if !tc.compile {
				require.Error(t, err,
					"a plain-name fragment with no registered anchor must not compile")

				return
			}

			require.NoError(t, err)

			err = v.Validate(t.Context(), "not an int")
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err,
					"the anchor ref must resolve and assert its target")
			}
		})
	}
}

// TestInlineDraft7AnchorKeywordsRegisterNothing pins the same draft gate
// through the inliner, which shares the refresolve registry walk: a Draft-07
// document whose only route to the ref target is a 2020-12 $anchor keyword
// reports the ref as unresolvable.
func TestInlineDraft7AnchorKeywordsRegisterNothing(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(stringtest.Input(`
		{
			"$schema": "http://json-schema.org/draft-07/schema#",
			"properties": {"x": {"$ref": "#a"}},
			"definitions": {"t": {"$anchor": "a", "type": "integer"}}
		}
	`)))
	require.NoError(t, err)

	_, err = jsonschema.Inline(t.Context(), schema)
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
}
