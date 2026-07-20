package jsonschema_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// missCountingResolver counts ResolveRef invocations per URI and answers every
// fetch with the configured error (or a plain miss when the error is nil).
type missCountingResolver struct {
	calls map[string]int
	err   error
}

func (r *missCountingResolver) ResolveRef(_ context.Context, uri string) (*jsonschema.Schema, error) {
	r.calls[uri]++

	return nil, r.err
}

func TestInlineFallbackFetchesUnresolvableDocumentOnce(t *testing.T) {
	t.Parallel()

	// With a fallback that continues past fetch failures, many nodes can
	// reference the same unresolvable URI; the per-run negative cache must keep
	// the resolver at one consultation per distinct URI, upholding the
	// documented at-most-once-per-call fetch contract, while every referencing
	// node still gets its own fallback consultation with the recorded failure.
	resolverErr := errors.New("upstream unreachable")

	tests := map[string]struct {
		resolverErr error
	}{
		"plain miss":     {},
		"resolver error": {resolverErr: resolverErr},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resolver := &missCountingResolver{calls: map[string]int{}, err: tt.resolverErr}

			var failures []jsonschema.RefFailure

			drop := jsonschema.RefFallbackFunc(func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
				failures = append(failures, f)

				return jsonschema.DropRef()
			})

			root, err := jsonschema.ParseSchema([]byte(stringtest.Input(`
				{
					"properties": {
						"a": {"$ref": "https://example.com/missing.json"},
						"b": {"$ref": "https://example.com/missing.json"},
						"c": {"$ref": "https://example.com/missing.json"}
					}
				}
			`)))
			require.NoError(t, err)

			_, err = jsonschema.Inline(t.Context(), root,
				jsonschema.WithRefResolver(resolver),
				jsonschema.WithRefFallback(drop))
			require.NoError(t, err)

			assert.Equal(t, map[string]int{"https://example.com/missing.json": 1}, resolver.calls,
				"an unresolvable document is fetched at most once per Inline call")

			require.Len(t, failures, 3, "every referencing node is consulted")

			for _, f := range failures {
				require.ErrorIs(t, f.Err, jsonschema.ErrRefResolve)

				if tt.resolverErr != nil {
					require.ErrorIs(t, f.Err, tt.resolverErr,
						"a recorded resolver error is replayed on later consultations")
				}
			}
		})
	}
}
