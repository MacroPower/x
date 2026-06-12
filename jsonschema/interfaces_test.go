package jsonschema_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestRefResolverFunc(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.RefResolverFunc(
		func(_ context.Context, uri string) (*jsonschema.Schema, bool, error) {
			if uri != "https://example.com/s.json" {
				return nil, false, fmt.Errorf("unexpected uri %q", uri)
			}

			return &jsonschema.Schema{Type: "string"}, true, nil
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
		func(_ context.Context, _ string) (*jsonschema.Schema, bool, error) {
			return nil, false, errBoom
		},
	)

	schema := &jsonschema.Schema{Ref: "https://example.com/missing.json"}

	err := jsonschema.Validate(t.Context(), schema, "ok", jsonschema.WithRefResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, errBoom)
}

// TestSchemaMap pins the map resolver contract: a hit returns the stored
// schema with ok true, while a missing URI or a nil stored schema reports
// ok false so the validator treats it as unresolved rather than failing.
func TestSchemaMap(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.SchemaMap{
		"https://example.com/s.json":   {Type: "string"},
		"https://example.com/nil.json": nil,
	}

	s, ok, err := resolver.ResolveRef(t.Context(), "https://example.com/s.json")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "string", s.Type)

	s, ok, err = resolver.ResolveRef(t.Context(), "https://example.com/missing.json")
	require.NoError(t, err)
	require.False(t, ok)
	assert.Nil(t, s)

	s, ok, err = resolver.ResolveRef(t.Context(), "https://example.com/nil.json")
	require.NoError(t, err)
	require.False(t, ok)
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
		func(context.Context, string) (*jsonschema.Schema, bool, error) {
			return nil, false, errBoom
		})

	t.Run("miss falls through to the first answer", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(nil, miss, hit, failing)

		s, ok, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "string", s.Type)
	})

	t.Run("error stops the chain", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(miss, failing, hit)

		_, _, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("all misses miss", func(t *testing.T) {
		t.Parallel()

		chain := jsonschema.ChainResolvers(nil, miss)

		s, ok, err := chain.ResolveRef(t.Context(), "https://example.com/s.json")
		require.NoError(t, err)
		require.False(t, ok)
		assert.Nil(t, s)
	})

	t.Run("empty chain misses", func(t *testing.T) {
		t.Parallel()

		s, ok, err := jsonschema.ChainResolvers().ResolveRef(t.Context(), "https://example.com/s.json")
		require.NoError(t, err)
		require.False(t, ok)
		assert.Nil(t, s)
	})
}

// TestDescriptionProviderFuncs pins the struct adapter contract: each
// function backs its method, and a nil function answers "" so the
// description stays unset for that half.
func TestDescriptionProviderFuncs(t *testing.T) {
	t.Parallel()

	type doc struct {
		Name string `json:"name"`
	}

	t.Run("both functions set", func(t *testing.T) {
		t.Parallel()

		provider := jsonschema.DescriptionProviderFuncs{
			TypeFunc: func(_ context.Context, tc jsonschema.TypeContext) string {
				return "type " + tc.Type.Name()
			},
			FieldFunc: func(_ context.Context, _ jsonschema.TypeContext, fieldName string) string {
				return "field " + fieldName
			},
		}

		s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithDescriptionProvider(provider))
		require.NoError(t, err)
		assert.Equal(t, "type doc", s.Description)
		assert.Equal(t, "field Name", s.Properties["name"].Description)
	})

	t.Run("nil functions leave descriptions unset", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithDescriptionProvider(jsonschema.DescriptionProviderFuncs{}))
		require.NoError(t, err)
		assert.Empty(t, s.Description)
		assert.Empty(t, s.Properties["name"].Description)
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
			func(_ context.Context, field jsonschema.FieldContext) error {
				field.Schema.Description = "in " + field.TagValue + " via " + field.TagKey
				return nil
			},
		)

		s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("units", interp))
		require.NoError(t, err)
		assert.Equal(t, "in meters via units", s.Properties["size"].Description)
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		errBad := errors.New("bad tag")
		interp := jsonschema.TagInterpreterFunc(
			func(context.Context, jsonschema.FieldContext) error { return errBad },
		)

		_, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("units", interp))
		require.ErrorIs(t, err, errBad)
		assert.ErrorContains(t, err, `tag interpreter "units"`)
	})
}

type ownerEmbedded struct {
	Inner string `json:"inner" units:"seconds"`
}

type ownerOuter struct {
	ownerEmbedded //nolint:unused // Exercised via reflection.

	Outer string `json:"outer" units:"meters"`
}

func TestTagInterpreterFieldContextOwner(t *testing.T) {
	t.Parallel()

	owners := map[string]reflect.Type{}
	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext) error {
			owners[field.Name] = field.Owner
			return nil
		},
	)

	_, err := jsonschema.GenerateFor[ownerOuter](t.Context(), jsonschema.WithTagInterpreter("units", interp))
	require.NoError(t, err)

	// A field declared on the struct itself is owned by that struct; a
	// promoted field is owned by the embedded type declaring it, matching
	// the type a DescriptionProvider receives for the same field.
	assert.Equal(t, reflect.TypeFor[ownerOuter](), owners["outer"])
	assert.Equal(t, reflect.TypeFor[ownerEmbedded](), owners["inner"])
}
