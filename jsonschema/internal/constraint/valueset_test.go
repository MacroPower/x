package constraint_test

import (
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

	t.Run("takes the free not slot", func(t *testing.T) {
		t.Parallel()

		var vs constraint.ValueSet

		forbidden := &jsonschema.Schema{MinItems: new(2), MaxItems: new(2)}
		vs.ForbidSchema(forbidden)

		s := renderValues(vs)
		assert.Same(t, forbidden, s.Not)
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

	t.Run("moves an existing subschema forbid under allOf", func(t *testing.T) {
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
