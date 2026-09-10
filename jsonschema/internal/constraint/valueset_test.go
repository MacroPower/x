package constraint_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

// renderValues writes a value set's forbidden state onto a fresh schema for
// inspection.
func renderValues(vs constraint.ValueSet) jsonschema.Schema {
	var s jsonschema.Schema

	vs.WriteForbidden(&s)

	return s
}

func TestValueSetForbidEscalation(t *testing.T) {
	t.Parallel()

	var vs constraint.ValueSet

	vs.Forbid(0)

	s := renderValues(vs)
	require.NotNil(t, s.Not)
	require.NotNil(t, s.Not.Const)
	assert.Equal(t, 0, *s.Not.Const)
	assert.Nil(t, s.Not.Enum)

	// A second distinct value promotes const to enum.
	vs.Forbid(1)

	s = renderValues(vs)
	assert.Nil(t, s.Not.Const)
	assert.Equal(t, []any{0, 1}, s.Not.Enum)

	// A third appends.
	vs.Forbid(2)

	s = renderValues(vs)
	assert.Equal(t, []any{0, 1, 2}, s.Not.Enum)

	// A duplicate (numeric-aware) is a no-op.
	vs.Forbid(int64(0))

	s = renderValues(vs)
	assert.Equal(t, []any{0, 1, 2}, s.Not.Enum)
}

func TestValueSetForbidNumericDedupAcrossTypes(t *testing.T) {
	t.Parallel()

	var vs constraint.ValueSet

	vs.Forbid(0)         // untyped int, as the required path forbids
	vs.Forbid(uint64(0)) // as ne=0 on an unsigned field forbids

	s := renderValues(vs)
	require.NotNil(t, s.Not)
	require.NotNil(t, s.Not.Const)
	assert.Nil(t, s.Not.Enum, "the same number must not become a two-member enum")
}

func TestValueSetForbidTakesTheNotSlotFromASiblingCarryingNot(t *testing.T) {
	t.Parallel()

	var vs constraint.ValueSet

	// A range not (from a collection ne) carries sibling keywords, so a further
	// forbidden value cannot merge into it. The value takes the not slot and the
	// range moves under allOf, whichever order the two arrive in. The keyword
	// table scopes not to the null wrapper and allOf to the value branch, so a
	// forbidden value that lost the slot would stop applying to a null instance.
	vs.ForbidSchema(&jsonschema.Schema{MinItems: new(2), MaxItems: new(2)})
	vs.Forbid(9)

	s := renderValues(vs)
	require.NotNil(t, s.Not, "the forbidden value keeps the not slot")
	require.NotNil(t, s.Not.Const)
	assert.Equal(t, 9, *s.Not.Const)
	require.Len(t, s.AllOf, 1)
	require.NotNil(t, s.AllOf[0].Not)
	assert.NotNil(t, s.AllOf[0].Not.MinItems, "the range moves under allOf")
}

func TestValueSetForbidSchemaLeavesForbiddenValuesInTheNotSlot(t *testing.T) {
	t.Parallel()

	var vs constraint.ValueSet

	// The mirror of the case above, arriving in the other order.
	vs.Forbid(9)
	vs.ForbidSchema(&jsonschema.Schema{MinItems: new(2), MaxItems: new(2)})

	s := renderValues(vs)
	require.NotNil(t, s.Not, "the forbidden value keeps the not slot")
	require.NotNil(t, s.Not.Const)
	assert.Equal(t, 9, *s.Not.Const)
	require.Len(t, s.AllOf, 1)
	require.NotNil(t, s.AllOf[0].Not)
	assert.NotNil(t, s.AllOf[0].Not.MinItems, "the range moves under allOf")
}

func TestValueSetForbidSchema(t *testing.T) {
	t.Parallel()

	t.Run("lands under allOf with the not slot free", func(t *testing.T) {
		t.Parallel()

		var vs constraint.ValueSet

		forbidden := &jsonschema.Schema{MinItems: new(2), MaxItems: new(2)}
		vs.ForbidSchema(forbidden)

		s := renderValues(vs)
		assert.Nil(t, s.Not, "a subschema forbid never takes the slot the null split reads")
		require.Len(t, s.AllOf, 1)
		assert.Same(t, forbidden, s.AllOf[0].Not)
	})

	t.Run("moves under allOf beside forbidden values", func(t *testing.T) {
		t.Parallel()

		var vs constraint.ValueSet

		vs.Forbid(0) // occupies the not slot

		forbidden := &jsonschema.Schema{MinItems: new(2), MaxItems: new(2)}
		vs.ForbidSchema(forbidden)

		s := renderValues(vs)
		require.NotNil(t, s.Not, "the forbidden value keeps the not slot")
		require.NotNil(t, s.Not.Const)
		assert.Equal(t, 0, *s.Not.Const)
		require.Len(t, s.AllOf, 1)
		assert.Same(t, forbidden, s.AllOf[0].Not)
	})

	t.Run("two subschema forbids each get their own not under allOf", func(t *testing.T) {
		t.Parallel()

		var vs constraint.ValueSet

		first := &jsonschema.Schema{MinItems: new(1)}
		second := &jsonschema.Schema{MaxItems: new(9)}

		vs.ForbidSchema(first)
		vs.ForbidSchema(second)

		s := renderValues(vs)
		assert.Nil(t, s.Not, "neither subschema forbid holds a slot the null split reads")
		require.Len(t, s.AllOf, 2)
		assert.Same(t, first, s.AllOf[0].Not)
		assert.Same(t, second, s.AllOf[1].Not)
	})

	t.Run("moves a seeded conjunction not under allOf", func(t *testing.T) {
		t.Parallel()

		var vs constraint.ValueSet

		seeded := &jsonschema.Schema{Const: new(any(5)), MinLength: new(3)}
		forbidden := &jsonschema.Schema{MaxItems: new(9)}

		vs.SeedNot(seeded)
		vs.ForbidSchema(forbidden)

		s := renderValues(vs)
		assert.Nil(t, s.Not, "a seeded not carrying sibling keywords cannot keep the slot")
		require.Len(t, s.AllOf, 2)
		assert.Equal(t, seeded, s.AllOf[0].Not, "the seeded not moves under allOf as a copy")
		assert.NotSame(t, seeded, s.AllOf[0].Not, "the seeded object itself is never written back")
		assert.Same(t, forbidden, s.AllOf[1].Not)
	})
}

// TestValueSetSeedNotLeavesTheSeededObjectUntouched pins that a forbid
// composes onto a copy of the seeded not rather than rewriting that object,
// the way ConjoinNot copies a type not. Forbid used to promote the seeded
// const to an enum and append to a seeded enum in place, so a schema an
// interpreter placed on several canvases accumulated every field's
// forbidden values.
func TestValueSetSeedNotLeavesTheSeededObjectUntouched(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		seeded *jsonschema.Schema
		forbid []any
		want   *jsonschema.Schema // the not written back
	}{
		"const promotes to an enum on the copy": {
			seeded: &jsonschema.Schema{Const: new(any(5))},
			forbid: []any{7},
			want:   &jsonschema.Schema{Enum: []any{5, 7}},
		},
		"enum grows on the copy": {
			seeded: &jsonschema.Schema{Enum: []any{5, 6}},
			forbid: []any{7, 8},
			want:   &jsonschema.Schema{Enum: []any{5, 6, 7, 8}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			before := *tc.seeded
			before.Enum = slices.Clone(tc.seeded.Enum)

			var vs constraint.ValueSet

			vs.SeedNot(tc.seeded)

			for _, v := range tc.forbid {
				vs.Forbid(v)
			}

			s := renderValues(vs)
			assert.Equal(t, tc.want, s.Not)
			assert.Equal(t, &before, tc.seeded, "the seeded object is unchanged")
			assert.NotSame(t, tc.seeded, s.Not)
		})
	}
}

func TestValueSetSeedNotWriteForbiddenPreservesOtherAllOf(t *testing.T) {
	t.Parallel()

	// A schema whose Not carries a sibling keyword, plus an unrelated allOf entry
	// (an embedded struct's branch). Forbidding a value must move that not under
	// allOf while leaving the pre-existing allOf entry in place.
	embed := &jsonschema.Schema{Ref: "#/$defs/Embedded"}
	s := &jsonschema.Schema{
		Not:   &jsonschema.Schema{Const: new(any(5)), MinLength: new(3)},
		AllOf: []*jsonschema.Schema{embed},
	}

	var vs constraint.ValueSet

	vs.SeedNot(s.Not)
	vs.Forbid(9)
	vs.WriteForbidden(s)

	require.NotNil(t, s.Not, "the forbidden value keeps the not slot")
	require.NotNil(t, s.Not.Const)
	assert.Equal(t, 9, *s.Not.Const)
	require.Len(t, s.AllOf, 2)
	assert.Same(t, embed, s.AllOf[0], "the pre-existing embed branch is preserved")
	require.NotNil(t, s.AllOf[1].Not)
	assert.NotNil(t, s.AllOf[1].Not.MinLength, "the sibling-carrying not moves under allOf")
}

func TestConjoinNot(t *testing.T) {
	t.Parallel()

	t.Run("const forbid folds into the type not", func(t *testing.T) {
		t.Parallel()

		typeNot := &jsonschema.Schema{Const: new(any("reserved"))}

		not, conjuncts := constraint.ConjoinNot(typeNot, &jsonschema.Schema{Const: new(any("x"))})

		require.NotNil(t, not)
		assert.Equal(t, []any{"reserved", "x"}, not.Enum)
		assert.Empty(t, conjuncts)
		assert.NotNil(t, typeNot.Const, "the type not is copied, never mutated")
		assert.Nil(t, typeNot.Enum, "the type not is copied, never mutated")
	})

	t.Run("enum forbid folds member by member", func(t *testing.T) {
		t.Parallel()

		typeNot := &jsonschema.Schema{Enum: []any{"a", "b"}}

		not, conjuncts := constraint.ConjoinNot(typeNot, &jsonschema.Schema{Enum: []any{"b", "c"}})

		require.NotNil(t, not)
		assert.Equal(t, []any{"a", "b", "c"}, not.Enum, "members dedup through the escalation")
		assert.Empty(t, conjuncts)
		assert.Equal(t, []any{"a", "b"}, typeNot.Enum, "the type not is copied, never mutated")
	})

	t.Run("sibling-carrying forbid moves under allOf", func(t *testing.T) {
		t.Parallel()

		typeNot := &jsonschema.Schema{Const: new(any("reserved"))}
		authored := &jsonschema.Schema{MinLength: new(3)}

		not, conjuncts := constraint.ConjoinNot(typeNot, authored)

		require.NotNil(t, not, "the type's forbidden value keeps the not slot")
		require.NotNil(t, not.Const)
		assert.Equal(t, "reserved", *not.Const)
		require.Len(t, conjuncts, 1)
		assert.Same(t, authored, conjuncts[0].Not, "the sibling-carrying forbid moves under allOf")
	})
}

// BenchmarkValueSetForbid measures accumulating distinct forbidden values,
// where each Forbid scans every member already held.
func BenchmarkValueSetForbid(b *testing.B) {
	for _, n := range []int{4, 32, 256} {
		vals := make([]any, n)
		for i := range vals {
			vals[i] = i
		}

		b.Run(fmt.Sprintf("distinct-%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				var vs constraint.ValueSet

				for _, v := range vals {
					vs.Forbid(v)
				}
			}
		})
	}
}
