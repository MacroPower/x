package constraint_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

func renderSet(t *testing.T, set *constraint.Set) jsonschema.Schema {
	t.Helper()

	var s jsonschema.Schema

	require.NoError(t, set.Render(&s))

	return s
}

// kindLower and kindUpper are the int8 kind-derived baseline bounds [-128, 127],
// the common Go-reflection baseline the tag and interpreter tiers merge against.
func kindLower() constraint.Bound {
	return lowerBound(incl(-128), constraint.Baseline, constraint.KindDerived)
}

func kindUpper() constraint.Bound {
	return upperBound(incl(127), constraint.Baseline, constraint.KindDerived)
}

func TestSetBaselineOnly(t *testing.T) {
	t.Parallel()

	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum)
	require.NotNil(t, s.Maximum)
	assert.InDelta(t, -128, *s.Minimum, 0)
	assert.InDelta(t, 127, *s.Maximum, 0)
}

func TestSetCanonicalizesCollapsingBounds(t *testing.T) {
	t.Parallel()

	// Decision 1: min=10 and gt=5 collapse to the single tighter keyword.
	set := constraint.New()
	set.AddNumeric(authoredLower(incl(10)))
	set.AddNumeric(authoredLower(excl(5)))

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, 10, *s.Minimum, 0)
	assert.Nil(t, s.ExclusiveMinimum, "the looser exclusive sibling is dropped")
}

func TestSetReplaceLastWins(t *testing.T) {
	t.Parallel()

	set := constraint.New()
	set.AddNumeric(lowerBound(incl(5), constraint.Replace, constraint.Authored))
	set.AddNumeric(lowerBound(incl(3), constraint.Replace, constraint.Authored))

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, 3, *s.Minimum, 0)
}

func TestSetReplaceWidensPastKind(t *testing.T) {
	t.Parallel()

	// A jsonschema-tag minimum overwrites the kind floor and can widen past it.
	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(lowerBound(incl(-200), constraint.Replace, constraint.Authored))

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, -200, *s.Minimum, 0)
}

func TestSetIntersectClampsIntoKindRange(t *testing.T) {
	t.Parallel()

	// A validate min=-300 on an int8 must not lower the -128 type floor.
	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(authoredLower(incl(-300)))

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, -128, *s.Minimum, 0)
}

func TestSetIntersectRaisesFloor(t *testing.T) {
	t.Parallel()

	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(authoredLower(incl(10)))

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, 10, *s.Minimum, 0)
}

func TestSetExclusiveBaselineReplacedByInclusive(t *testing.T) {
	t.Parallel()

	// An int64 kind ceiling is exclusive 2^63; a maximum=100 replaces it and the
	// canonical result is the inclusive keyword.
	set := constraint.New()
	set.AddNumeric(upperBound(exclBig("9223372036854775808"), constraint.Baseline, constraint.KindDerived))
	set.AddNumeric(upperBound(incl(100), constraint.Replace, constraint.Authored))

	s := renderSet(t, set)
	require.NotNil(t, s.Maximum)
	assert.InDelta(t, 100, *s.Maximum, 0)
	assert.Nil(t, s.ExclusiveMaximum)
}

func TestSetConstSubsumesAllNumericBounds(t *testing.T) {
	t.Parallel()

	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())
	set.AddNumeric(authoredLower(incl(0))) // an authored bound too
	require.NoError(t, set.Values.SetConst(5))

	s := renderSet(t, set)
	assert.Nil(t, s.Minimum)
	assert.Nil(t, s.Maximum)
	require.NotNil(t, s.Const)
	assert.Equal(t, 5, *s.Const)
}

func TestSetEnumDropsKindKeepsAuthored(t *testing.T) {
	t.Parallel()

	// Decision 3 + doc.go:438-443: an enum drops the kind-derived bounds but
	// keeps an authored bound that narrows it (enum=10|20 + gte=15 -> only 20).
	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())
	set.AddNumeric(authoredLower(incl(15)))
	require.NoError(t, set.Values.SetEnum([]any{10, 20}))

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum, "the authored floor is kept")
	assert.InDelta(t, 15, *s.Minimum, 0)
	assert.Nil(t, s.Maximum, "the kind-derived ceiling is dropped")
	assert.Equal(t, []any{10, 20}, s.Enum)
}

func TestSetEnumDropsBareKindBounds(t *testing.T) {
	t.Parallel()

	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())
	require.NoError(t, set.Values.SetEnum([]any{1, 2}))

	s := renderSet(t, set)
	assert.Nil(t, s.Minimum)
	assert.Nil(t, s.Maximum)
}

func TestSetUnsatisfiableNumericPreserved(t *testing.T) {
	t.Parallel()

	// A min=10,max=5 pair stays an impossible range, never loosened.
	set := constraint.New()
	set.AddNumeric(authoredLower(incl(10)))
	set.AddNumeric(authoredUpper(incl(5)))

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum)
	require.NotNil(t, s.Maximum)
	assert.InDelta(t, 10, *s.Minimum, 0)
	assert.InDelta(t, 5, *s.Maximum, 0)
}

func TestSetSizeRendering(t *testing.T) {
	t.Parallel()

	t.Run("min floor", func(t *testing.T) {
		t.Parallel()

		set := constraint.New()
		bounds, err := constraint.ParseSizeBound("5", constraint.RuleMin, constraint.Intersect, constraint.Authored)
		require.NoError(t, err)
		set.AddSize(constraint.Length, bounds)

		s := renderSet(t, set)
		require.NotNil(t, s.MinLength)
		assert.Equal(t, 5, *s.MinLength)
		assert.Nil(t, s.MaxLength)
	})

	t.Run("lt zero unsatisfiable", func(t *testing.T) {
		t.Parallel()

		set := constraint.New()
		bounds, err := constraint.ParseSizeBound("0", constraint.RuleLt, constraint.Intersect, constraint.Authored)
		require.NoError(t, err)
		set.AddSize(constraint.Length, bounds)

		s := renderSet(t, set)
		require.NotNil(t, s.MinLength)
		require.NotNil(t, s.MaxLength)
		assert.Equal(t, 1, *s.MinLength)
		assert.Equal(t, 0, *s.MaxLength)
	})

	t.Run("len below max yields impossible range", func(t *testing.T) {
		t.Parallel()

		set := constraint.New()
		maxB, err := constraint.ParseSizeBound("3", constraint.RuleMax, constraint.Intersect, constraint.Authored)
		require.NoError(t, err)

		lenB, err := constraint.ParseSizeBound("5", constraint.RuleLen, constraint.Intersect, constraint.Authored)
		require.NoError(t, err)
		set.AddSize(constraint.Items, maxB)
		set.AddSize(constraint.Items, lenB)

		s := renderSet(t, set)
		require.NotNil(t, s.MinItems)
		require.NotNil(t, s.MaxItems)
		assert.Equal(t, 5, *s.MinItems)
		assert.Equal(t, 3, *s.MaxItems)
	})
}

func TestSetMultipleOfSurvivesConst(t *testing.T) {
	t.Parallel()

	// The const/enum subsumption never touches multipleOf, so a const keeps it.
	set := constraint.New()
	set.SetMultipleOf(4)
	require.NoError(t, set.Values.SetConst(6))

	s := renderSet(t, set)
	require.NotNil(t, s.MultipleOf)
	assert.InDelta(t, 4, *s.MultipleOf, 0)
	require.NotNil(t, s.Const)
}

func TestSetRenderOrderIndependent(t *testing.T) {
	t.Parallel()

	build := func(order []constraint.Bound) jsonschema.Schema {
		set := constraint.New()
		for _, b := range order {
			set.AddNumeric(b)
		}

		var s jsonschema.Schema

		require.NoError(t, set.Render(&s))

		return s
	}

	forward := build([]constraint.Bound{
		kindLower(),
		lowerBound(incl(0), constraint.Replace, constraint.Authored),
		authoredLower(incl(20)),
	})
	reverse := build([]constraint.Bound{
		authoredLower(incl(20)),
		lowerBound(incl(0), constraint.Replace, constraint.Authored),
		kindLower(),
	})

	assert.Equal(t, forward, reverse)
}

func TestSetAbsorbSchemaRoundTrip(t *testing.T) {
	t.Parallel()

	// A source that only produces a *Schema still converges into the model.
	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())

	seed := &jsonschema.Schema{Minimum: new(10.0)}
	require.NoError(t, set.AbsorbSchema(seed, constraint.Intersect, constraint.Authored))

	s := renderSet(t, set)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, 10, *s.Minimum, 0, "the absorbed floor tightens the kind range")
	require.NotNil(t, s.Maximum)
	assert.InDelta(t, 127, *s.Maximum, 0)
}

func TestSetAbsorbSchemaConstConflict(t *testing.T) {
	t.Parallel()

	set := constraint.New()
	require.NoError(t, set.Values.SetConst(5))

	err := set.AbsorbSchema(&jsonschema.Schema{Const: new(any(6))}, constraint.Intersect, constraint.Authored)
	require.ErrorIs(t, err, constraint.ErrConflict)
}

func TestCanonicalizeNumeric(t *testing.T) {
	t.Parallel()

	t.Run("collapses min beside gt to the tighter minimum", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{Minimum: new(10.0), ExclusiveMinimum: new(5.0)}
		constraint.CanonicalizeNumeric(s)
		require.NotNil(t, s.Minimum)
		assert.InDelta(t, 10, *s.Minimum, 0)
		assert.Nil(t, s.ExclusiveMinimum)
	})

	t.Run("keeps a lone exclusive bound", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{ExclusiveMaximum: new(3.0)}
		constraint.CanonicalizeNumeric(s)
		require.NotNil(t, s.ExclusiveMaximum)
		assert.InDelta(t, 3, *s.ExclusiveMaximum, 0)
		assert.Nil(t, s.Maximum)
	})

	t.Run("leaves the int64 kind bounds unchanged", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{Minimum: new(-9223372036854775808.0), ExclusiveMaximum: new(9223372036854775808.0)}
		constraint.CanonicalizeNumeric(s)
		require.NotNil(t, s.Minimum)
		require.NotNil(t, s.ExclusiveMaximum)
		assert.InDelta(t, -9223372036854775808.0, *s.Minimum, 0)
		assert.InDelta(t, 9223372036854775808.0, *s.ExclusiveMaximum, 0)
	})

	t.Run("no-op on a schema with no numeric bounds", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{MinLength: new(3)}
		constraint.CanonicalizeNumeric(s)
		assert.Nil(t, s.Minimum)
		require.NotNil(t, s.MinLength)
	})
}
