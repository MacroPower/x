package constraint_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

// renderBounds resolves the set under mode and renders the bounds onto a fresh
// schema, the exact call sequence the reconcile path performs.
func renderBounds(set *constraint.Set, mode constraint.ResolveMode) jsonschema.Schema {
	var s jsonschema.Schema

	resolved := set.ResolveBounds(mode)
	resolved.RenderBounds(&s)

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

	s := renderBounds(set, constraint.ResolveKeepKind)
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

	s := renderBounds(set, constraint.ResolveKeepKind)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, 10, *s.Minimum, 0)
	assert.Nil(t, s.ExclusiveMinimum, "the looser exclusive sibling is dropped")
}

func TestSetIntersectClampsIntoKindRange(t *testing.T) {
	t.Parallel()

	// A validate min=-300 on an int8 must not lower the -128 type floor.
	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(authoredLower(incl(-300)))

	s := renderBounds(set, constraint.ResolveKeepKind)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, -128, *s.Minimum, 0)
}

func TestSetIntersectRaisesFloor(t *testing.T) {
	t.Parallel()

	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(authoredLower(incl(10)))

	s := renderBounds(set, constraint.ResolveKeepKind)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, 10, *s.Minimum, 0)
}

func TestSetExclusiveKindCeilingTightenedByInclusive(t *testing.T) {
	t.Parallel()

	// An int64 kind ceiling is exclusive 2^63; an authored maximum=100 tightens
	// it and the canonical result is the single inclusive keyword.
	set := constraint.New()
	set.AddNumeric(upperBound(exclBig("9223372036854775808"), constraint.Baseline, constraint.KindDerived))
	set.AddNumeric(authoredUpper(incl(100)))

	s := renderBounds(set, constraint.ResolveKeepKind)
	require.NotNil(t, s.Maximum)
	assert.InDelta(t, 100, *s.Maximum, 0)
	assert.Nil(t, s.ExclusiveMaximum)
}

func TestSetDropAllSubsumesEveryNumericBound(t *testing.T) {
	t.Parallel()

	// The mode a field const (or a pinned element) selects: every numeric bound,
	// kind-derived and authored alike, is redundant against a pinned value.
	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())
	set.AddNumeric(authoredLower(incl(0))) // an authored bound too

	s := renderBounds(set, constraint.ResolveDropAll)
	assert.Nil(t, s.Minimum)
	assert.Nil(t, s.Maximum)
}

func TestSetDropKindKeepsAuthored(t *testing.T) {
	t.Parallel()

	// The mode a field enum selects (doc.go: enum=10|20 + gte=15 -> only 20): the
	// kind-derived bounds are dropped by provenance while an authored bound that
	// narrows the enum survives, even one equal in value to a kind bound.
	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())
	set.AddNumeric(authoredLower(incl(15)))

	s := renderBounds(set, constraint.ResolveDropKind)
	require.NotNil(t, s.Minimum, "the authored floor is kept")
	assert.InDelta(t, 15, *s.Minimum, 0)
	assert.Nil(t, s.Maximum, "the kind-derived ceiling is dropped")
}

func TestSetDropKindDropsBareKindBounds(t *testing.T) {
	t.Parallel()

	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())

	s := renderBounds(set, constraint.ResolveDropKind)
	assert.Nil(t, s.Minimum)
	assert.Nil(t, s.Maximum)
}

func TestSetUnsatisfiableNumericPreserved(t *testing.T) {
	t.Parallel()

	// A min=10,max=5 pair stays an impossible range, never loosened.
	set := constraint.New()
	set.AddNumeric(authoredLower(incl(10)))
	set.AddNumeric(authoredUpper(incl(5)))

	s := renderBounds(set, constraint.ResolveKeepKind)
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

		s := renderBounds(set, constraint.ResolveKeepKind)
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

		s := renderBounds(set, constraint.ResolveKeepKind)
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

		s := renderBounds(set, constraint.ResolveKeepKind)
		require.NotNil(t, s.MinItems)
		require.NotNil(t, s.MaxItems)
		assert.Equal(t, 5, *s.MinItems)
		assert.Equal(t, 3, *s.MaxItems)
	})

	t.Run("size axes fold under every numeric mode", func(t *testing.T) {
		t.Parallel()

		// The const/enum subsumption applies to the numeric axis only: a size
		// bound survives a DropAll resolve.
		set := constraint.New()
		bounds, err := constraint.ParseSizeBound("5", constraint.RuleMin, constraint.Intersect, constraint.Authored)
		require.NoError(t, err)
		set.AddSize(constraint.Items, bounds)

		s := renderBounds(set, constraint.ResolveDropAll)
		require.NotNil(t, s.MinItems)
		assert.Equal(t, 5, *s.MinItems)
	})
}

func TestSetMultipleOfSurvivesDropAll(t *testing.T) {
	t.Parallel()

	// The const/enum subsumption never touches multipleOf: a divisibility rule
	// stays meaningful under a pinned value.
	set := constraint.New()
	set.SetMultipleOf(4)

	s := renderBounds(set, constraint.ResolveDropAll)
	require.NotNil(t, s.MultipleOf)
	assert.InDelta(t, 4, *s.MultipleOf, 0)
}

func TestSetResolveOrderIndependent(t *testing.T) {
	t.Parallel()

	build := func(order []constraint.Bound) jsonschema.Schema {
		set := constraint.New()
		for _, b := range order {
			set.AddNumeric(b)
		}

		return renderBounds(set, constraint.ResolveKeepKind)
	}

	forward := build([]constraint.Bound{
		kindLower(),
		authoredLower(incl(0)),
		authoredLower(incl(20)),
	})
	reverse := build([]constraint.Bound{
		authoredLower(incl(20)),
		authoredLower(incl(0)),
		kindLower(),
	})

	assert.Equal(t, forward, reverse)
}

func TestSetAbsorbAxesIntersects(t *testing.T) {
	t.Parallel()

	// The reconcile entry point: a schema's bounds absorbed at the Intersect tier
	// tighten the kind baseline and never widen it.
	set := constraint.New()
	set.AddNumeric(kindLower())
	set.AddNumeric(kindUpper())

	canvas := &jsonschema.Schema{Minimum: new(10.0), Maximum: new(200.0)}
	set.AbsorbAxes(canvas, constraint.Intersect, constraint.Authored)

	s := renderBounds(set, constraint.ResolveKeepKind)
	require.NotNil(t, s.Minimum)
	assert.InDelta(t, 10, *s.Minimum, 0, "the absorbed floor tightens the kind range")
	require.NotNil(t, s.Maximum)
	assert.InDelta(t, 127, *s.Maximum, 0, "the weaker absorbed ceiling cannot widen the kind ceiling")
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
