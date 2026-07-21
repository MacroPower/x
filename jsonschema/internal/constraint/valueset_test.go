package constraint_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

// renderValues renders a value set onto a fresh schema for inspection.
func renderValues(vs constraint.ValueSet) jsonschema.Schema {
	var s jsonschema.Schema

	vs.Render(&s)

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

func TestValueSetForbidEscalatesToAllOfWhenNotHasSiblings(t *testing.T) {
	t.Parallel()

	var vs constraint.ValueSet

	// A range not (from a collection ne) carries sibling keywords, so a further
	// forbidden value cannot merge into it and both move under allOf.
	vs.ForbidSchema(&jsonschema.Schema{MinItems: new(2), MaxItems: new(2)})
	vs.Forbid(9)

	s := renderValues(vs)
	assert.Nil(t, s.Not, "the existing not moves under allOf")
	require.Len(t, s.AllOf, 2)
	require.NotNil(t, s.AllOf[1].Not)
	require.NotNil(t, s.AllOf[1].Not.Const)
	assert.Equal(t, 9, *s.AllOf[1].Not.Const)
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

	t.Run("moves an existing not under allOf", func(t *testing.T) {
		t.Parallel()

		var vs constraint.ValueSet

		vs.Forbid(0) // occupies the not slot

		forbidden := &jsonschema.Schema{MinItems: new(2), MaxItems: new(2)}
		vs.ForbidSchema(forbidden)

		s := renderValues(vs)
		assert.Nil(t, s.Not)
		require.Len(t, s.AllOf, 2)
		assert.Same(t, forbidden, s.AllOf[1].Not)
	})
}

func TestValueSetSetConstConflict(t *testing.T) {
	t.Parallel()

	var vs constraint.ValueSet

	require.NoError(t, vs.SetConst(5))
	require.NoError(t, vs.SetConst(uint64(5)), "the same number in a different type is not a conflict")

	err := vs.SetConst(6)
	require.ErrorIs(t, err, constraint.ErrConflict)
}

func TestValueSetSetEnumConflict(t *testing.T) {
	t.Parallel()

	var vs constraint.ValueSet

	require.NoError(t, vs.SetEnum([]any{1, 2}))

	err := vs.SetEnum([]any{3})
	require.ErrorIs(t, err, constraint.ErrConflict)
}

func TestValueSetRender(t *testing.T) {
	t.Parallel()

	var vs constraint.ValueSet

	require.NoError(t, vs.SetConst("x"))
	vs.Forbid(0)

	s := renderValues(vs)
	require.NotNil(t, s.Const)
	assert.Equal(t, "x", *s.Const)
	require.NotNil(t, s.Not)
	require.NotNil(t, s.Not.Const)
	assert.Equal(t, 0, *s.Not.Const)
}

func TestValueSetSeedNotWriteForbiddenPreservesOtherAllOf(t *testing.T) {
	t.Parallel()

	// A schema whose Not carries a sibling keyword, plus an unrelated allOf entry
	// (an embedded struct's branch). Forbidding a value must escalate the not
	// under allOf while leaving the pre-existing allOf entry in place.
	embed := &jsonschema.Schema{Ref: "#/$defs/Embedded"}
	s := &jsonschema.Schema{
		Not:   &jsonschema.Schema{Const: new(any(5)), MinLength: new(3)},
		AllOf: []*jsonschema.Schema{embed},
	}

	var vs constraint.ValueSet

	vs.SeedNot(s.Not)
	vs.Forbid(9)
	vs.WriteForbidden(s)

	assert.Nil(t, s.Not, "the sibling-carrying not moves under allOf")
	require.Len(t, s.AllOf, 3)
	assert.Same(t, embed, s.AllOf[0], "the pre-existing embed branch is preserved")
	require.NotNil(t, s.AllOf[2].Not)
	require.NotNil(t, s.AllOf[2].Not.Const)
	assert.Equal(t, 9, *s.AllOf[2].Not.Const)
}
