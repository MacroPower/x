package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestTagConstOutsideEnumConflicts pins that a const and an enum that exclude
// each other abort generation with [jsonschema.ErrConstraintConflict] rather
// than composing silently into a schema no instance satisfies. Both keywords
// fully describe the allowed set and assert conjunctively, so an excluded pin
// is a contradiction, in either order and in either dialect.
func TestTagConstOutsideEnumConflicts(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func() (*jsonschema.Schema, error){
		"const then enum": func() (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" jsonschema:"const=7,enum=5|6"`
			}

			return jsonschema.GenerateFor[T](t.Context())
		},
		"enum then const": func() (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" jsonschema:"enum=5|6,const=7"`
			}

			return jsonschema.GenerateFor[T](t.Context())
		},
		"validate eq then oneof": func() (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" validate:"eq=7,oneof=5 6"`
			}

			return jsonschema.GenerateFor[T](t.Context(),
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		},
		"validate oneof then eq": func() (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" validate:"oneof=5 6,eq=7"`
			}

			return jsonschema.GenerateFor[T](t.Context(),
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := build()
			require.Error(t, err)
			require.ErrorIs(t, err, jsonschema.ErrConstraintConflict)
		})
	}
}

// TestTagConstInsideEnumComposes pins the satisfiable half: a const the
// enumeration admits is not a conflict, in either order.
func TestTagConstInsideEnumComposes(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func() (*jsonschema.Schema, error){
		"const then enum": func() (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" jsonschema:"const=5,enum=5|6"`
			}

			return jsonschema.GenerateFor[T](t.Context())
		},
		"enum then const": func() (*jsonschema.Schema, error) {
			type T struct {
				V int `json:"v" jsonschema:"enum=5|6,const=5"`
			}

			return jsonschema.GenerateFor[T](t.Context())
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := build()
			require.NoError(t, err)
			assert.NotNil(t, s.Properties["v"].Const)
			assert.Len(t, s.Properties["v"].Enum, 2)
		})
	}
}
