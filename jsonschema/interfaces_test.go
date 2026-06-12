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

	require.NoError(t, jsonschema.Validate(t.Context(), schema, "ok", jsonschema.WithResolver(resolver)))

	err := jsonschema.Validate(t.Context(), schema, float64(5), jsonschema.WithResolver(resolver))

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

	err := jsonschema.Validate(t.Context(), schema, "ok", jsonschema.WithResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, errBoom)
}

func TestTagInterpreterFunc(t *testing.T) {
	t.Parallel()

	type doc struct {
		Size int `json:"size" units:"meters"`
	}

	t.Run("interprets the named tag", func(t *testing.T) {
		t.Parallel()

		interp := jsonschema.TagInterpreterFunc("units",
			func(tag string, field jsonschema.FieldContext) error {
				field.Schema.Description = "in " + tag
				return nil
			},
		)

		s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter(interp))
		require.NoError(t, err)
		assert.Equal(t, "in meters", s.Properties["size"].Description)
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		errBad := errors.New("bad tag")
		interp := jsonschema.TagInterpreterFunc("units",
			func(string, jsonschema.FieldContext) error { return errBad },
		)

		_, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter(interp))
		require.ErrorIs(t, err, errBad)
		assert.ErrorContains(t, err, `tag interpreter "units"`)
	})
}
