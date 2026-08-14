package jsonschema_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// hookTypedLevels is a named nilable container whose extender writes the
// natural, redundant Type into the bare payload; the null encoding on its
// extracted def body must fold that authored type into the type list.
type hookTypedLevels []string

func (hookTypedLevels) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value.Type = "array"

	return nil
}

// TestReconcileHookAuthoredTypeSlot pins that the null encoding honors a
// hook-authored Type or Types on a nilable container's bare payload instead
// of stacking its own write beside it. An extender may set the natural,
// redundant type slot ("add, remove, or modify any fields"); the encoding
// must still emit exactly one of Type/Types, or the generated schema fails
// to marshal ("both Type and Types are set") far from the cause.
func TestReconcileHookAuthoredTypeSlot(t *testing.T) {
	t.Parallel()

	type doc struct {
		T []string `json:"t"`
	}

	t.Run("authored Type folds into the null type list", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTypeSchemaExtenderFor[[]string](
				func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
					ts.Value.Type = "array"

					return nil
				}),
		)
		require.NoError(t, err)

		prop := s.Properties["t"]
		require.Empty(t, prop.Type)
		require.Equal(t, []string{"null", "array"}, prop.Types)

		_, err = json.Marshal(s)
		require.NoError(t, err)
	})

	t.Run("authored Types survives the bare container type restore", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithNullable(false),
			jsonschema.WithTypeSchemaExtenderFor[[]string](
				func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
					ts.Value.Types = []string{"array"}

					return nil
				}),
		)
		require.NoError(t, err)

		prop := s.Properties["t"]
		require.Empty(t, prop.Type)
		require.Equal(t, []string{"array"}, prop.Types)

		_, err = json.Marshal(s)
		require.NoError(t, err)
	})

	t.Run("authored Type on an extracted def body", func(t *testing.T) {
		t.Parallel()

		type named struct {
			L hookTypedLevels `json:"l"`
		}

		s, err := jsonschema.GenerateFor[named](t.Context())
		require.NoError(t, err)

		require.Len(t, s.Defs, 1)

		for _, def := range s.Defs {
			require.Empty(t, def.Type)
			require.Equal(t, []string{"null", "array"}, def.Types)
		}

		_, err = json.Marshal(s)
		require.NoError(t, err)
	})
}
