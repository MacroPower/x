package jsonschema_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// lateResolver answers [jsonschema.ErrNotResolved] until armed, then serves its
// fixed document: the shape of a resolver whose documents become available only
// after compilation, which Compile explicitly tolerates.
type lateResolver struct {
	doc   *jsonschema.Schema
	armed atomic.Bool
}

// ResolveRef serves the fixed document once armed, and a miss before that.
func (r *lateResolver) ResolveRef(_ context.Context, _ string) (*jsonschema.Schema, error) {
	if !r.armed.Load() {
		return nil, jsonschema.ErrNotResolved
	}

	return r.doc, nil
}

// TestValidateLateFetchedRemoteStructuralChecks locks in that a remote document
// first fetched at validation time is vetted with the same structural checks
// Compile applies to compile-time-fetched remotes. Without them a late-fetched
// document with a negative bound rejects every instance, and one with a
// Draft-07 items array under a 2020-12 run silently accepts every element.
func TestValidateLateFetchedRemoteStructuralChecks(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   string
		doc      string
		instance string
		err      error
		valid    bool
	}{
		"negative bound in late-fetched document": {
			schema:   `{"$ref": "https://example.test/late.json"}`,
			doc:      `{"maxItems": -1}`,
			instance: `[]`,
			err:      jsonschema.ErrNegativeBound,
		},
		"items array in late-fetched document under 2020-12": {
			schema:   `{"$ref": "https://example.test/late.json"}`,
			doc:      `{"items": [{"type": "string"}]}`,
			instance: `[123]`,
			err:      jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"invalid type name in late-fetched document": {
			schema:   `{"$ref": "https://example.test/late.json"}`,
			doc:      `{"type": "strng"}`,
			instance: `"hello"`,
			err:      jsonschema.ErrInvalidType,
		},
		"items array in late-fetched document under draft-07 keeps tuple semantics": {
			schema:   `{"$schema": "http://json-schema.org/draft-07/schema#", "$ref": "https://example.test/late.json"}`,
			doc:      `{"items": [{"type": "string"}]}`,
			instance: `["ok"]`,
			valid:    true,
		},
		"well-formed late-fetched document validates": {
			schema:   `{"$ref": "https://example.test/late.json"}`,
			doc:      `{"type": "string"}`,
			instance: `"hello"`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := &lateResolver{doc: doc}

			v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err, "a resolver miss at compile time is tolerated")

			resolver.armed.Store(true)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.ErrorIs(t, err, jsonschema.ErrRefResolve,
				"a structurally invalid late-fetched document must fail the ref loudly")
			require.ErrorIs(t, err, tc.err,
				"the structural sentinel must stay reachable through the validation error")
		})
	}
}

// TestValidateLateFetchedRemoteIDCollision pins that a document first fetched
// during a validation run is held to the identifier rule Compile applies to a
// compile-time fetch. The late document claims the root's own URI, so the two
// documents leave every reference naming it ambiguous. Compile tolerates the
// miss, and the run that reaches the reference reports the collision rather
// than letting the late document take the root's URI for the rest of the run.
func TestValidateLateFetchedRemoteIDCollision(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://example.test/root.json", "$ref": "https://example.test/late.json"}`,
	))
	require.NoError(t, err)

	// The document carries both faults, so the assertions below pin which one
	// the run names.
	doc, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://example.test/root.json", "type": "strnig"}`,
	))
	require.NoError(t, err)

	resolver := &lateResolver{doc: doc}

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err, "a resolver miss at compile time is tolerated")

	resolver.armed.Store(true)

	err = v.ValidateJSON(t.Context(), []byte(`"hello"`))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision,
		"the late document claims the URI the root holds")
	require.ErrorIs(t, err, jsonschema.ErrRefResolve,
		"the collision surfaces through the referencing ref")
	require.NotErrorIs(t, err, jsonschema.ErrInvalidType,
		"the collision is settled ahead of the structural vet, which the misspelled type would fail")
}
