package jsonschema_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestCompileRefWalkRejectsUnresolvableRefs locks in the strict side of the
// compile-time reference walk: a reference that resolves to nothing while its
// document is present can never resolve later, so Compile reports it with
// [jsonschema.ErrNotResolved], naming the bearing node's schema path. The
// walk covers every node of the root document, referenced or not, and
// $dynamicRef resolves statically under the same policy.
func TestCompileRefWalkRejectsUnresolvableRefs(t *testing.T) {
	t.Parallel()

	remote := jsonschema.SchemaMap{
		"http://example.com/present.json": {Type: "string"},
	}

	tests := map[string]struct {
		schema string
		opts   []jsonschema.ValidateOption
		err    error
		want   string
	}{
		"unresolvable local pointer ref": {
			schema: `{"$ref": "#/nope"}`,
			err:    jsonschema.ErrNotResolved,
			want:   `cannot resolve $ref "#/nope"`,
		},
		"unresolvable anchor ref": {
			schema: `{"properties": {"a": {"$ref": "#missing"}}}`,
			err:    jsonschema.ErrNotResolved,
			want:   "/properties/a",
		},
		"broken ref in unreferenced defs": {
			schema: `{"$defs": {"unused": {"$ref": "#/also/nope"}}}`,
			err:    jsonschema.ErrNotResolved,
			want:   "/$defs/unused",
		},
		"unresolvable dynamic ref": {
			schema: `{"$dynamicRef": "#nothing"}`,
			err:    jsonschema.ErrNotResolved,
			want:   `cannot resolve $dynamicRef "#nothing"`,
		},
		"unparsable ref": {
			schema: `{"$ref": "://bad"}`,
			err:    jsonschema.ErrNotResolved,
			want:   `cannot resolve $ref "://bad"`,
		},
		"broken fragment into a fetched document": {
			schema: `{"$ref": "http://example.com/present.json#/missing"}`,
			opts:   []jsonschema.ValidateOption{jsonschema.WithRefResolver(remote)},
			err:    jsonschema.ErrNotResolved,
			want:   `cannot resolve $ref "http://example.com/present.json#/missing"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema, tc.opts...)
			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.want,
				"the error must name the bearing node and the reference")
		})
	}
}

// TestCompileToleratesResolverErrorAtCompile pins the Remote References
// contract's tolerant side: a resolver failure at compile time is a document
// miss, so Compile succeeds, and the validation walk reports the ref with the
// resolver's error wrapping [jsonschema.ErrRefResolve].
func TestCompileToleratesResolverErrorAtCompile(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "http://example.com/broken.json"}`))
	require.NoError(t, err)

	resolver := jsonschema.RefResolverFunc(func(context.Context, string) (*jsonschema.Schema, error) {
		return nil, errors.New("backend unavailable")
	})

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err, "a resolver failure at compile time defers to the validation walk")

	err = v.Validate(t.Context(), "anything")
	require.ErrorIs(t, err, jsonschema.ErrRefResolve,
		"the validation walk must report the deferred resolver failure")
}

// TestCompileFetchCountsOncePerURI pins the once-per-URI resolver contract
// across the pipeline: the compile-time reference walk fetches the document
// into the shared registry, so two later validation runs resolve it from
// cache without another resolver call.
func TestCompileFetchCountsOncePerURI(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	resolver := jsonschema.RefResolverFunc(func(context.Context, string) (*jsonschema.Schema, error) {
		calls.Add(1)

		return &jsonschema.Schema{Type: "string"}, nil
	})

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "http://example.com/s.json"}`))
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	require.NoError(t, v.Validate(t.Context(), "hello"))
	require.NoError(t, v.Validate(t.Context(), "world"))

	assert.Equal(t, int64(1), calls.Load(),
		"the resolver must be consulted once per URI across Compile and every Validate")
}
