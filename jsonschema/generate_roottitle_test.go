package jsonschema_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// Tests for WithRootTitle: deriving the root schema's title from the
// generated root type's name.

// rootTitleStruct is a named root type for title derivation.
type rootTitleStruct struct {
	Name string `json:"name"`
}

// rootTitleRecursive references itself, so its root schema stays a $defs
// (definitions for Draft-07) entry referenced from the root.
type rootTitleRecursive struct {
	Name string              `json:"name"`
	Next *rootTitleRecursive `json:"next,omitempty"`
}

// rootTitleExtender sets its own title via JSONSchemaExtend.
type rootTitleExtender struct {
	Name string `json:"name"`
}

func (rootTitleExtender) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
	s.Title = "Extended"

	return nil
}

func TestWithRootTitle(t *testing.T) {
	t.Parallel()

	t.Run("named struct root", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		assert.Equal(t, "rootTitleStruct", s.Title)
	})

	t.Run("defaults to off", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context())
		require.NoError(t, err)

		assert.Empty(t, s.Title)
	})

	t.Run("pointer root", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[*rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		// A pointer root generates a nullable anyOf wrapper; the title is
		// derived from the pointer-dereferenced type and sits on the root.
		assert.Equal(t, "rootTitleStruct", s.Title)
	})

	t.Run("self-referential Draft-07 root titles the definitions entry", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleRecursive](t.Context(),
			jsonschema.WithDraft(jsonschema.Draft7),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		// Draft-07 readers ignore keywords beside $ref, so a title on the
		// bare $ref root would be invisible; it lands on the definitions
		// entry instead, shared by every occurrence of the type.
		require.Equal(t, "#/definitions/rootTitleRecursive", s.Ref)
		assert.Empty(t, s.Title, "the bare $ref root carries no sibling title")

		def := s.Definitions["rootTitleRecursive"]
		require.NotNil(t, def)
		assert.Equal(t, "rootTitleRecursive", def.Title)
	})

	t.Run("self-referential Draft 2020-12 root titles the root", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleRecursive](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		// Draft 2020-12 honors $ref siblings, so the title sits on the root
		// $ref node and the $defs entry stays untitled.
		require.Equal(t, "#/$defs/rootTitleRecursive", s.Ref)
		assert.Equal(t, "rootTitleRecursive", s.Title)

		def := s.Defs["rootTitleRecursive"]
		require.NotNil(t, def)
		assert.Empty(t, def.Title)
	})

	t.Run("anonymous struct root has no title", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[struct {
			Name string `json:"name"`
		}](t.Context(), jsonschema.WithRootTitle(true))
		require.NoError(t, err)

		assert.Empty(t, s.Title, "an unnamed root type yields no name to title")
	})

	t.Run("unnamed map root has no title", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[map[string]int](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		assert.Empty(t, s.Title)
	})

	t.Run("existing title preserved", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
			jsonschema.WithTypeSchema(
				reflect.TypeFor[rootTitleStruct](),
				&jsonschema.Schema{Type: "object", Title: "Custom"},
			),
		)
		require.NoError(t, err)

		assert.Equal(t, "Custom", s.Title, "WithTypeSchema title is never overwritten")
	})

	t.Run("extender title preserved", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleExtender](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		assert.Equal(t, "Extended", s.Title, "JSONSchemaExtend title is never overwritten")
	})

	t.Run("custom namer honored", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
			jsonschema.WithNamer(jsonschema.NamerFunc(func(t reflect.Type) string {
				return "My" + t.Name()
			})),
		)
		require.NoError(t, err)

		assert.Equal(t, "MyrootTitleStruct", s.Title)
	})

	t.Run("definitions disabled", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
			jsonschema.WithDefinitions(false),
		)
		require.NoError(t, err)

		// With WithDefinitions(false) the root carries no $id or $defs name,
		// so the derived title is the only place the type name appears.
		assert.Equal(t, "rootTitleStruct", s.Title)
		assert.Empty(t, s.Defs)
	})
}
