package jsonschema_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// boundInterp returns a tag interpreter that runs fn against the field's
// Constraints facade, so a test drives the public contribution surface through a
// real generation run (the only place element and reconcile behavior is live).
func boundInterp(fn func(c *jsonschema.Constraints) error) jsonschema.TagInterpreterFunc {
	return func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
		return fn(field.Constraints())
	}
}

// TestConstraintsFacadeValueSet exercises the value-set half of the public
// Constraints facade end to end: a tag interpreter that pins a const, forbids a
// value, and reads the pinned state back through the getters.
func TestConstraintsFacadeValueSet(t *testing.T) {
	t.Parallel()

	type Payload struct {
		Name string `json:"name" pin:"x"`
	}

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			c := field.Constraints()

			_, ok := c.Const()
			assert.False(t, ok, "no const is pinned before the interpreter sets one")

			require.NoError(t, c.SetConst("x"))

			got, ok := c.Const()
			require.True(t, ok)
			assert.Equal(t, "x", got)

			c.Forbid("y")

			return nil
		},
	)

	s, err := jsonschema.GenerateFor[Payload](t.Context(),
		jsonschema.WithTagInterpreter("pin", interp),
	)
	require.NoError(t, err)

	field := s.Properties["name"]
	require.NotNil(t, field.Const)
	assert.Equal(t, "x", *field.Const)
	require.NotNil(t, field.Not)
	require.NotNil(t, field.Not.Const)
	assert.Equal(t, "y", *field.Not.Const)
}

// TestConstraintsFacadeConflictSentinel confirms a conflicting second const
// surfaces through the exported [jsonschema.ErrConstraintConflict] sentinel.
func TestConstraintsFacadeConflictSentinel(t *testing.T) {
	t.Parallel()

	type Payload struct {
		Name string `json:"name" pin:"x"`
	}

	var conflict error

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			c := field.Constraints()
			require.NoError(t, c.SetConst("a"))
			require.NoError(t, c.SetConst("a"), "the same value is not a conflict")

			conflict = c.SetConst("b")

			return nil
		},
	)

	_, err := jsonschema.GenerateFor[Payload](t.Context(),
		jsonschema.WithTagInterpreter("pin", interp),
	)
	require.NoError(t, err)
	require.ErrorIs(t, conflict, jsonschema.ErrConstraintConflict,
		"a different second const surfaces the public conflict sentinel")
}

// TestConstraintsFacadeNumericBoundPolicy confirms the facade applies the shared
// 2^53 exact-representability policy through the public sentinel.
func TestConstraintsFacadeNumericBoundPolicy(t *testing.T) {
	t.Parallel()

	type Payload struct {
		Count int64 `bound:"x" json:"count"`
	}

	var (
		okErr  error
		bigErr error
	)

	interp := boundInterp(func(c *jsonschema.Constraints) error {
		okErr = c.AddNumericBound(jsonschema.BoundMin, "10")
		bigErr = c.AddNumericBound(jsonschema.BoundMax, "9007199254740993")

		return nil
	})

	_, err := jsonschema.GenerateFor[Payload](t.Context(),
		jsonschema.WithTagInterpreter("bound", interp),
	)
	require.NoError(t, err)
	require.NoError(t, okErr)
	require.ErrorIs(t, bigErr, jsonschema.ErrBoundNotRepresentable,
		"a bound beyond 2^53 is rejected as not exactly representable")
}

// TestConstraintsFacadeMultipleOfPositive confirms SetMultipleOf rejects a
// non-positive value.
func TestConstraintsFacadeMultipleOfPositive(t *testing.T) {
	t.Parallel()

	type Payload struct {
		Ratio float64 `json:"ratio" mult:"x"`
	}

	var negErr error

	interp := boundInterp(func(c *jsonschema.Constraints) error {
		require.NoError(t, c.SetMultipleOf(4))

		negErr = c.SetMultipleOf(0)

		return nil
	})

	s, err := jsonschema.GenerateFor[Payload](t.Context(),
		jsonschema.WithTagInterpreter("mult", interp),
	)
	require.NoError(t, err)
	require.Error(t, negErr)

	field := s.Properties["ratio"]
	require.NotNil(t, field.MultipleOf)
	assert.InDelta(t, 4, *field.MultipleOf, 0)
}

// TestConstraintsFacadeIntersectOnly confirms the bound methods only tighten: a
// bound wider than the kind bound is a no-op, so the kind bound survives, while a
// tighter bound is applied.
func TestConstraintsFacadeIntersectOnly(t *testing.T) {
	t.Parallel()

	type Payload struct {
		N int8 `bound:"x" json:"n"`
	}

	interp := boundInterp(func(c *jsonschema.Constraints) error {
		// A floor below the int8 kind floor cannot loosen it.
		require.NoError(t, c.AddNumericBound(jsonschema.BoundMin, "-200"))
		// A ceiling tighter than the kind ceiling is applied.
		require.NoError(t, c.AddNumericBound(jsonschema.BoundMax, "50"))

		return nil
	})

	s, err := jsonschema.GenerateFor[Payload](t.Context(),
		jsonschema.WithTagInterpreter("bound", interp),
	)
	require.NoError(t, err)

	field := s.Properties["n"]
	require.NotNil(t, field.Minimum)
	assert.InDelta(t, -128, *field.Minimum, 0, "the kind floor survives a wider tag bound")
	require.NotNil(t, field.Maximum)
	assert.InDelta(t, 50, *field.Maximum, 0, "the tighter ceiling is applied")
}

// TestConstraintsFacadeCountIntersection confirms AddCountBound targets the
// array count keywords and intersects them, and that an unsatisfiable count
// (lt=0) is preserved as the floor-one/ceiling-zero range no array satisfies.
func TestConstraintsFacadeCountIntersection(t *testing.T) {
	t.Parallel()

	t.Run("intersects a range", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			Items []int `count:"x" json:"items"`
		}

		interp := boundInterp(func(c *jsonschema.Constraints) error {
			require.NoError(t, c.AddCountBound(jsonschema.LenMin, "2"))
			require.NoError(t, c.AddCountBound(jsonschema.LenMax, "5"))

			return nil
		})

		s, err := jsonschema.GenerateFor[Payload](t.Context(),
			jsonschema.WithTagInterpreter("count", interp),
		)
		require.NoError(t, err)

		field := s.Properties["items"]
		require.NotNil(t, field.MinItems)
		require.NotNil(t, field.MaxItems)
		assert.Equal(t, 2, *field.MinItems)
		assert.Equal(t, 5, *field.MaxItems)
	})

	t.Run("preserves an unsatisfiable count", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			Items []int `count:"x" json:"items"`
		}

		interp := boundInterp(func(c *jsonschema.Constraints) error {
			return c.AddCountBound(jsonschema.LenLt, "0")
		})

		s, err := jsonschema.GenerateFor[Payload](t.Context(),
			jsonschema.WithTagInterpreter("count", interp),
		)
		require.NoError(t, err)

		field := s.Properties["items"]
		require.NotNil(t, field.MinItems)
		require.NotNil(t, field.MaxItems)
		assert.Equal(t, 1, *field.MinItems)
		assert.Equal(t, 0, *field.MaxItems)
	})
}

// TestConstraintsFacadeBoundSurvivesEnum covers a facade-authored numeric bound
// under a field enum: the bound survives because a field enum drops the
// kind-derived bounds but keeps an authored bound that narrows the enum.
func TestConstraintsFacadeBoundSurvivesEnum(t *testing.T) {
	t.Parallel()

	type Payload struct {
		Score int `bound:"x" json:"score" jsonschema:"enum=10|20"`
	}

	interp := boundInterp(func(c *jsonschema.Constraints) error {
		return c.AddNumericBound(jsonschema.BoundMin, "15")
	})

	s, err := jsonschema.GenerateFor[Payload](t.Context(),
		jsonschema.WithTagInterpreter("bound", interp),
	)
	require.NoError(t, err)

	field := s.Properties["score"]
	assert.Equal(t, []any{int64(10), int64(20)}, field.Enum)
	require.NotNil(t, field.Minimum, "the facade bound survives the enum")
	assert.InDelta(t, 15, *field.Minimum, 0)
}

// TestConstraintsCanonicalizeRedundantSibling confirms the algebra collapses a
// redundant sibling keyword: a jsonschema minimum=10 beside a validate gt=5
// resolves to the single tighter minimum, dropping the looser exclusiveMinimum.
func TestConstraintsCanonicalizeRedundantSibling(t *testing.T) {
	t.Parallel()

	type Payload struct {
		N int `json:"n" jsonschema:"minimum=10" validate:"gt=5"`
	}

	s, err := jsonschema.GenerateFor[Payload](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	field := s.Properties["n"]
	require.NotNil(t, field.Minimum)
	assert.InDelta(t, 10, *field.Minimum, 0)
	assert.Nil(t, field.ExclusiveMinimum, "the looser gt sibling collapses into the tighter minimum")
}

// TestConstraintsNullableSplitKeywordSideChange covers the value-branch output
// change from Section 3: a kind Minimum=0 (uint8) plus an authored gt renders as
// ExclusiveMinimum on the wrapper and clears the kind Minimum from the value
// branch rather than restoring it. The authored endpoint subsumes the kind one,
// so the value branch carries no floor.
func TestConstraintsNullableSplitKeywordSideChange(t *testing.T) {
	t.Parallel()

	type Payload struct {
		N *uint8 `json:"n" jsonschema:"exclusiveMinimum=5"`
	}

	s, err := jsonschema.GenerateFor[Payload](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s.Properties["n"])
	require.NoError(t, err)

	// ExclusiveMinimum rides the wrapper; the value branch keeps only the kind
	// ceiling, with no minimum restored (the authored gt subsumes the kind 0).
	assert.JSONEq(t,
		`{"exclusiveMinimum":5,"anyOf":[{"type":"integer","maximum":255},{"type":"null"}]}`,
		string(got))
}

// TestConstraintsDraft7RefSiblingBound confirms a facade-contributed bound on a
// $ref-rendered Draft-07 field moves into the allOf beside the $ref rather than
// sitting as an ignored sibling: the algebra writes the bound onto merged before
// renderRef performs the Draft-07 sibling wrap.
func TestConstraintsDraft7RefSiblingBound(t *testing.T) {
	t.Parallel()

	type Container struct {
		Level NonStructProvider `json:"level" validate:"gt=0"`
	}

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	field := s.Properties["level"]
	require.Empty(t, field.Ref, "the bare $ref must be wrapped, not left beside the bound")
	require.Len(t, field.AllOf, 1)
	assert.Equal(t, "#/definitions/NonStructProvider", field.AllOf[0].Ref)
	require.NotNil(t, field.ExclusiveMinimum, "the bound stays a sibling of allOf")
	assert.InDelta(t, 0, *field.ExclusiveMinimum, 0)
}

// TestConstraintsInterpreterOrderIndependent confirms two interpreters each
// contributing a bound through the facade produce the identical schema
// regardless of their registration order.
func TestConstraintsInterpreterOrderIndependent(t *testing.T) {
	t.Parallel()

	type Payload struct {
		N int32 `hi:"x" json:"n" lo:"x"`
	}

	lo := boundInterp(func(c *jsonschema.Constraints) error {
		return c.AddNumericBound(jsonschema.BoundMin, "10")
	})
	hi := boundInterp(func(c *jsonschema.Constraints) error {
		return c.AddNumericBound(jsonschema.BoundMax, "100")
	})

	gen := func(opts ...jsonschema.GenerateOption) *jsonschema.Schema {
		s, err := jsonschema.GenerateFor[Payload](t.Context(), opts...)
		require.NoError(t, err)

		return s.Properties["n"]
	}

	forward := gen(
		jsonschema.WithTagInterpreter("lo", lo),
		jsonschema.WithTagInterpreter("hi", hi),
	)
	reverse := gen(
		jsonschema.WithTagInterpreter("hi", hi),
		jsonschema.WithTagInterpreter("lo", lo),
	)

	assert.Equal(t, forward, reverse)
	require.NotNil(t, forward.Minimum)
	require.NotNil(t, forward.Maximum)
	assert.InDelta(t, 10, *forward.Minimum, 0)
	assert.InDelta(t, 100, *forward.Maximum, 0)
}
