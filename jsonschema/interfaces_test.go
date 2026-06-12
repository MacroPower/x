package jsonschema_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestRefResolverFunc(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.RefResolverFunc(
		func(_ context.Context, uri string) (*jsonschema.Schema, error) {
			if uri != "https://example.com/s.json" {
				return nil, fmt.Errorf("unexpected uri %q", uri)
			}

			return &jsonschema.Schema{Type: "string"}, nil
		},
	)

	schema := &jsonschema.Schema{Ref: "https://example.com/s.json"}

	require.NoError(t, jsonschema.Validate(t.Context(), schema, "ok", jsonschema.WithRefResolver(resolver)))

	err := jsonschema.Validate(t.Context(), schema, float64(5), jsonschema.WithRefResolver(resolver))

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)
}

func TestRefResolverFunc_Error(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	resolver := jsonschema.RefResolverFunc(
		func(_ context.Context, _ string) (*jsonschema.Schema, error) {
			return nil, errBoom
		},
	)

	schema := &jsonschema.Schema{Ref: "https://example.com/missing.json"}

	err := jsonschema.Validate(t.Context(), schema, "ok", jsonschema.WithRefResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, errBoom)
}

// TestSchemaMap pins the map resolver contract: a hit returns the stored
// schema, a miss returns (nil, nil) so the validator treats it as
// unresolved rather than failing.
func TestSchemaMap(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.SchemaMap{
		"https://example.com/s.json": {Type: "string"},
	}

	s, err := resolver.ResolveRef(t.Context(), "https://example.com/s.json")
	require.NoError(t, err)
	assert.Equal(t, "string", s.Type)

	s, err = resolver.ResolveRef(t.Context(), "https://example.com/missing.json")
	require.NoError(t, err)
	assert.Nil(t, s)
}

// TestChainResolvers pins the chain contract: nil links are skipped, a miss
// falls through to the next link, the first schema or error answers, and a
// chain of misses is itself a miss.
func TestChainResolvers(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	miss := jsonschema.SchemaMap{}
	hit := jsonschema.SchemaMap{"https://example.com/s.json": {Type: "string"}}
	failing := jsonschema.RefResolverFunc(
		func(context.Context, string) (*jsonschema.Schema, error) {
			return nil, errBoom
		})

	t.Run("miss falls through to the first answer", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(nil, miss, hit, failing)

		s, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.NoError(t, err)
		assert.Equal(t, "string", s.Type)
	})

	t.Run("error stops the chain", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(miss, failing, hit)

		_, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("all misses miss", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(nil, miss)

		s, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.NoError(t, err)
		assert.Nil(t, s)
	})

	t.Run("empty chain misses", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.ChainResolvers().ResolveRef(t.Context(), "https://example.com/s.json")
		require.NoError(t, err)
		assert.Nil(t, s)
	})
}

func TestTagInterpreterFunc(t *testing.T) {
	t.Parallel()

	type doc struct {
		Size int `json:"size" units:"meters"`
	}

	t.Run("interprets the registered tag", func(t *testing.T) {
		t.Parallel()

		interp := jsonschema.TagInterpreterFunc(
			func(tag string, field jsonschema.FieldContext) error {
				field.Schema.Description = "in " + tag
				return nil
			},
		)

		s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("units", interp))
		require.NoError(t, err)
		assert.Equal(t, "in meters", s.Properties["size"].Description)
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		errBad := errors.New("bad tag")
		interp := jsonschema.TagInterpreterFunc(
			func(string, jsonschema.FieldContext) error { return errBad },
		)

		_, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("units", interp))
		require.ErrorIs(t, err, errBad)
		assert.ErrorContains(t, err, `tag interpreter "units"`)
	})
}
