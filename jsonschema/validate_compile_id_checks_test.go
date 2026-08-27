package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestCompileChecksIDDomain locks in the compile-time $id domain checks: a
// fragment in $id is rejected under Draft 2020-12 (core section 8.2.1) and
// tolerated under Draft-07 (the anchor spelling), an $id beside a $ref is
// ignored under Draft-07, an unparsable $id is rejected, and a checked $id
// must resolve to an absolute URI against its enclosing base. The base chain
// is the parent $id hierarchy, or [jsonschema.WithBaseURI] for the root: the
// same relative root $id that fails bare compiles once a base supplies the
// absolute prefix.
func TestCompileChecksIDDomain(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		opts   []jsonschema.ValidateOption
		err    error
		path   string
	}{
		"2020-12 fragment-only id rejected": {
			schema: `{"$defs": {"a": {"$id": "#frag", "type": "integer"}}}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$defs/a/$id",
		},
		"2020-12 fragment-carrying id rejected": {
			schema: `{"$id": "http://example.com/root.json#frag", "type": "string"}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$id",
		},
		"draft-07 fragment-only id compiles": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"definitions": {"a": {"$id": "#frag", "type": "integer"}},
				"allOf": [{"$ref": "#frag"}]
			}`,
		},
		"draft-07 id beside ref is ignored": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"definitions": {"t": {"type": "integer"}},
				"allOf": [{"$ref": "#/definitions/t", "$id": "relative.json"}]
			}`,
		},
		"relative root id without base rejected": {
			schema: `{"$id": "sub/schema.json", "type": "string"}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$id",
		},
		"relative root id under draft-07 without base rejected": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "sub/schema.json",
				"type": "string"
			}`,
			err:  jsonschema.ErrInvalidID,
			path: "/$id",
		},
		"relative root id with base compiles": {
			// The compile-accept side of the same schema: WithBaseURI supplies
			// the absolute prefix the relative $id resolves against.
			schema: `{"$id": "sub/schema.json", "type": "string"}`,
			opts:   []jsonschema.ValidateOption{jsonschema.WithBaseURI("http://example.com/root.json")},
		},
		"nested relative id against absolute parent compiles": {
			schema: `{
				"$id": "http://example.com/root.json",
				"$defs": {"a": {"$id": "item.json", "type": "string"}}
			}`,
		},
		"nested relative id without any base rejected": {
			schema: `{"$defs": {"a": {"$id": "item.json", "type": "string"}}}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$defs/a/$id",
		},
		"unparsable id rejected": {
			schema: `{"$id": "http://example.com/a%zz", "type": "string"}`,
			err:    jsonschema.ErrInvalidID,
			path:   "/$id",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema, tc.opts...)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.path,
				"the violation must name the offending $id's schema path")
		})
	}
}

// TestCompileRejectsInvalidBaseURI pins that an unparsable
// [jsonschema.WithBaseURI] value fails Compile with
// [jsonschema.ErrInvalidBaseURI] instead of silently corrupting every
// registry key derived from the base.
func TestCompileRejectsInvalidBaseURI(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"type": "string"}`))
	require.NoError(t, err)

	_, err = jsonschema.Compile(t.Context(), schema, jsonschema.WithBaseURI("://bad"))
	require.ErrorIs(t, err, jsonschema.ErrInvalidBaseURI)
}

// TestCompileChecksVocabularyPlacement locks in the $vocabulary placement
// check: a node carrying $vocabulary needs a $schema that establishes the
// Draft 2020-12 dialect. The match is exact, so the trailing-"#" spelling of
// the 2020-12 URI is rejected, and an empty $schema is rejected under
// Draft-07, which predates the vocabulary concept.
func TestCompileChecksVocabularyPlacement(t *testing.T) {
	t.Parallel()

	// A remote document carrying $vocabulary with no $schema of its own,
	// served for the fetched-document cases below.
	remote := jsonschema.SchemaMap{
		"http://example.com/vocab.json": {
			Vocabulary: map[string]bool{"https://json-schema.org/draft/2020-12/vocab/core": true},
			Type:       "object",
		},
	}

	tests := map[string]struct {
		schema string
		opts   []jsonschema.ValidateOption
		err    error
	}{
		"vocabulary without a schema under 2020-12 compiles": {
			// The empty $schema inherits the run's dialect, the reading
			// upstream applied to loaded documents.
			schema: `{
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
		},
		"fetched document vocabulary with empty schema compiles under 2020-12": {
			schema: `{"$ref": "http://example.com/vocab.json"}`,
			opts:   []jsonschema.ValidateOption{jsonschema.WithRefResolver(remote)},
		},
		"fetched document vocabulary with empty schema rejected under draft-07": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$ref": "http://example.com/vocab.json"
			}`,
			opts: []jsonschema.ValidateOption{jsonschema.WithRefResolver(remote)},
			err:  jsonschema.ErrMisplacedVocabulary,
		},
		"vocabulary under the exact 2020-12 schema URI compiles": {
			schema: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
		},
		"vocabulary under the trailing-hash 2020-12 spelling rejected": {
			schema: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema#",
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
			err: jsonschema.ErrMisplacedVocabulary,
		},
		"vocabulary under the draft-07 schema URI rejected": {
			schema: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
			err: jsonschema.ErrMisplacedVocabulary,
		},
		"vocabulary without a schema under draft-07 rejected": {
			schema: `{
				"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
				"type": "object"
			}`,
			opts: []jsonschema.ValidateOption{jsonschema.WithDraft(jsonschema.Draft7)},
			err:  jsonschema.ErrMisplacedVocabulary,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema, tc.opts...)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err)
		})
	}
}
