package schemashape_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

func TestClearNumericBounds(t *testing.T) {
	t.Parallel()

	s := &jsonschema.Schema{
		Minimum:          new(1.0),
		Maximum:          new(2.0),
		ExclusiveMinimum: new(0.0),
		ExclusiveMaximum: new(3.0),
	}

	schemashape.ClearNumericBounds(s)

	assert.Nil(t, s.Minimum)
	assert.Nil(t, s.Maximum)
	assert.Nil(t, s.ExclusiveMinimum)
	assert.Nil(t, s.ExclusiveMaximum)
}

func TestMoveConstEnum(t *testing.T) {
	t.Parallel()

	c := new(any(5.0))
	src := &jsonschema.Schema{Const: c, Enum: []any{1, 2}}
	dst := &jsonschema.Schema{}

	schemashape.MoveConstEnum(src, dst)

	assert.Nil(t, src.Const)
	assert.Nil(t, src.Enum)
	assert.Equal(t, c, dst.Const)
	assert.Equal(t, []any{1, 2}, dst.Enum)
}

func TestNullableTypeListBase(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		types    []string
		wantBase string
		wantOK   bool
	}{
		"null first":  {types: []string{"null", "string"}, wantBase: "string", wantOK: true},
		"null second": {types: []string{"string", "null"}, wantBase: "string", wantOK: true},
		"single type": {types: []string{"string"}, wantBase: "", wantOK: false},
		"no null":     {types: []string{"string", "integer"}, wantBase: "", wantOK: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base, ok := schemashape.NullableTypeListBase(&jsonschema.Schema{Types: tc.types})
			assert.Equal(t, tc.wantBase, base)
			assert.Equal(t, tc.wantOK, ok)
		})
	}
}

func TestRelocateConstEnumToValueBranch(t *testing.T) {
	t.Parallel()

	t.Run("neither const nor enum is unchanged", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{Type: "integer"}
		assert.Same(t, s, schemashape.RelocateConstEnumToValueBranch(s))
	})

	t.Run("non-nullable keeps const in place", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{Type: "integer", Const: new(any(5.0))}
		got := schemashape.RelocateConstEnumToValueBranch(s)
		assert.Same(t, s, got)
		assert.NotNil(t, s.Const)
	})

	t.Run("anyOf wrapper moves const to value branch", func(t *testing.T) {
		t.Parallel()

		value := &jsonschema.Schema{Type: "integer"}
		s := &jsonschema.Schema{
			AnyOf: []*jsonschema.Schema{value, {Type: "null"}},
			Const: new(any(5.0)),
		}

		got := schemashape.RelocateConstEnumToValueBranch(s)
		assert.Same(t, value, got)
		assert.NotNil(t, value.Const)
		assert.Nil(t, s.Const)
	})

	t.Run("type list is rewritten into anyOf", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{Types: []string{"null", "integer"}, Enum: []any{1, 2}}

		got := schemashape.RelocateConstEnumToValueBranch(s)

		require.Len(t, s.AnyOf, 2)
		assert.Same(t, s.AnyOf[0], got)
		assert.Nil(t, s.Types)
		assert.Nil(t, s.Enum)
		assert.Equal(t, "integer", got.Type)
		assert.Equal(t, []any{1, 2}, got.Enum)
		assert.Equal(t, "null", s.AnyOf[1].Type)
	})
}

func TestDropTypeBoundsForConstEnum(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema        *jsonschema.Schema
		boundAuthored bool
		wantCleared   bool
	}{
		"const clears bounds": {
			schema: &jsonschema.Schema{
				Type:    "integer",
				Const:   new(any(5.0)),
				Minimum: new(1.0),
				Maximum: new(10.0),
			},
			wantCleared: true,
		},
		"enum drops kind-derived bounds": {
			schema:      &jsonschema.Schema{Type: "integer", Enum: []any{1, 2}, Minimum: new(1.0)},
			wantCleared: true,
		},
		"enum keeps author-set bound": {
			schema:        &jsonschema.Schema{Type: "integer", Enum: []any{1, 2}, Minimum: new(1.0)},
			boundAuthored: true,
			wantCleared:   false,
		},
		"no const or enum keeps bounds": {
			schema:      &jsonschema.Schema{Type: "integer", Minimum: new(1.0)},
			wantCleared: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schemashape.DropTypeBoundsForConstEnum(tc.schema, tc.boundAuthored)

			if tc.wantCleared {
				assert.Nil(t, tc.schema.Minimum)
				assert.Nil(t, tc.schema.Maximum)
			} else {
				assert.NotNil(t, tc.schema.Minimum)
			}
		})
	}
}
