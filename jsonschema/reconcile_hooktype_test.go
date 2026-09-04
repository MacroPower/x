package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// hookTypedLevels is a named container whose extender declares the type
// nullable and writes the natural, redundant Type into the bare payload; the
// null encoding on its extracted def body must fold that authored type into
// the type list.
type hookTypedLevels []string

func (hookTypedLevels) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value.Type = "array"
	ts.Nullability = jsonschema.NullAllowed

	return nil
}

// TestReconcileHookAuthoredTypeSlot pins that the null encoding honors a
// hook-authored Type or Types on a nullable occurrence's bare payload instead
// of stacking its own write beside it. An extender may set the natural,
// redundant type slot ("add, remove, or modify any fields"); the encoding
// must still emit exactly one of Type/Types, or the generated schema fails
// to marshal ("both Type and Types are set") far from the cause. A container
// is not nullable on its own under encoding/json/v2, so the null admission
// comes from the extender's [jsonschema.NullAllowed] stance.
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
					ts.Nullability = jsonschema.NullAllowed

					return nil
				},
			),
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
			jsonschema.WithTypeSchemaExtenderFor[[]string](
				func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
					ts.Value.Types = []string{"array"}

					return nil
				},
			),
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

		// An extracted def keeps the bare payload with the authored Type; the
		// stance's null admission rides each reference's anyOf wrapper instead
		// of the def body.
		for _, def := range s.Defs {
			require.Equal(t, "array", def.Type)
			require.Empty(t, def.Types)
		}

		prop := s.Properties["l"]
		require.Len(t, prop.AnyOf, 2)
		require.Equal(t, "null", prop.AnyOf[1].Type)

		_, err = json.Marshal(s)
		require.NoError(t, err)
	})
}
