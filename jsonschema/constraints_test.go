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

// TestConstraintsFacadeBaseCrossCheck confirms the SetConst/SetEnum backstop
// consults the type-derived base like it does the canvas: reconcile overlays the
// canvas const/enum onto the payload, so a value the field's type already pins
// would be silently overwritten if the facade only checked the canvas.
func TestConstraintsFacadeBaseCrossCheck(t *testing.T) {
	t.Parallel()

	t.Run("const conflicts with a type-pinned const", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			Name string `json:"name" pin:"x"`
		}

		var conflict error

		interp := boundInterp(func(c *jsonschema.Constraints) error {
			conflict = c.SetConst("other")

			return nil
		})

		typed := any("typed")

		_, err := jsonschema.GenerateFor[Payload](t.Context(),
			jsonschema.WithTagInterpreter("pin", interp),
			jsonschema.WithTypeSchemaFor[string](jsonschema.TypeSchema{
				Value: &jsonschema.Schema{Type: "string", Const: &typed},
			}),
		)
		require.NoError(t, err)
		require.ErrorIs(t, conflict, jsonschema.ErrConstraintConflict,
			"a const disagreeing with the type-pinned one is a conflict, not an override")
	})

	t.Run("const equal to the type-pinned const is not a conflict", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			Name string `json:"name" pin:"x"`
		}

		interp := boundInterp(func(c *jsonschema.Constraints) error {
			return c.SetConst("typed")
		})

		typed := any("typed")

		s, err := jsonschema.GenerateFor[Payload](t.Context(),
			jsonschema.WithTagInterpreter("pin", interp),
			jsonschema.WithTypeSchemaFor[string](jsonschema.TypeSchema{
				Value: &jsonschema.Schema{Type: "string", Const: &typed},
			}),
		)
		require.NoError(t, err)

		field := s.Properties["name"]
		require.NotNil(t, field.Const)
		assert.Equal(t, "typed", *field.Const)
	})

	t.Run("enum conflicts with a type-set enum", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			Name string `json:"name" pin:"x"`
		}

		var conflict error

		interp := boundInterp(func(c *jsonschema.Constraints) error {
			conflict = c.SetEnum([]any{"a", "b"})

			return nil
		})

		_, err := jsonschema.GenerateFor[Payload](t.Context(),
			jsonschema.WithTagInterpreter("pin", interp),
			jsonschema.WithTypeSchemaFor[string](jsonschema.TypeSchema{
				Value: &jsonschema.Schema{Type: "string", Enum: []any{"a", "b", "c"}},
			}),
		)
		require.NoError(t, err)
		require.ErrorIs(t, conflict, jsonschema.ErrConstraintConflict,
			"a second enumeration cannot silently shadow the type's own")
	})
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

	t.Run("rejects a non-container kind", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			N int `count:"x" json:"n"`
		}

		var countErr error

		interp := boundInterp(func(c *jsonschema.Constraints) error {
			countErr = c.AddCountBound(jsonschema.LenMin, "2")

			return nil
		})

		s, err := jsonschema.GenerateFor[Payload](t.Context(),
			jsonschema.WithTagInterpreter("count", interp),
		)
		require.NoError(t, err)
		require.Error(t, countErr,
			"an int field has no count keyword to target")

		field := s.Properties["n"]
		assert.Nil(t, field.MinItems, "no stray minItems lands on a non-container schema")
		assert.Nil(t, field.MinProperties)
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

// pinnedDefStruct is a named struct (always $defs-extracted) carrying a
// type-pinned const through WithTypeSchemaFor in
// TestConstraintsSetConstExtractedTypeComposes.
type pinnedDefStruct struct{}

// TestConstraintsSetConstExtractedTypeComposes pins the documented
// $defs-extracted behavior of the SetConst backstop: the referenced
// definition's const lives in the def, not on the field's provisional {$ref}
// base, so SetConst reports no conflict; the canvas const rides beside the
// $ref and both apply conjunctively, composing disagreeing values to a
// faithfully unsatisfiable schema instead of aborting generation.
func TestConstraintsSetConstExtractedTypeComposes(t *testing.T) {
	t.Parallel()

	type doc struct {
		F pinnedDefStruct `json:"f" pin:"b"`
	}

	var setErr error

	interp := boundInterp(func(c *jsonschema.Constraints) error {
		setErr = c.SetConst("b")

		return nil
	})

	pinned := any("a")

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTagInterpreter("pin", interp),
		jsonschema.WithTypeSchemaFor[pinnedDefStruct](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{Type: "string", Const: &pinned},
		}),
	)
	require.NoError(t, err)
	require.NoError(t, setErr,
		"a def-pinned const is not visible on the {$ref} base, so no conflict is reported")

	field := s.Properties["f"]
	require.NotNil(t, field)
	assert.NotEmpty(t, field.Ref, "the extracted type stays referenced")
	require.NotNil(t, field.Const)
	assert.Equal(t, "b", *field.Const, "the canvas const rides beside the $ref")

	for _, instance := range []map[string]any{{"f": "a"}, {"f": "b"}} {
		require.Error(t, jsonschema.Validate(t.Context(), s, instance),
			"instance %v must fail: the def const and the sibling const conjoin", instance)
	}
}

// reservedNotString carries a type-level not through WithTypeSchemaFor in
// TestConstraintsForbidComposesTypeNot.
type reservedNotString string

// TestConstraintsForbidComposesTypeNot pins that a field-level forbid composes
// with a type-derived not instead of replacing it. The overlay used to
// blind-assign the canvas not over the type's own, so a field-level Forbid let
// the type-forbidden value validate on a non-nullable field while the nullable
// split kept both -- the same tag accepted "reserved" or not depending on
// pointer-ness.
func TestConstraintsForbidComposesTypeNot(t *testing.T) {
	t.Parallel()

	reserved := any("reserved")
	typeSchema := func() jsonschema.GenerateOption {
		return jsonschema.WithTypeSchemaFor[reservedNotString](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{
				Type: "string",
				Not:  &jsonschema.Schema{Const: &reserved},
			},
		})
	}

	t.Run("forbid value folds into the type not", func(t *testing.T) {
		t.Parallel()

		type doc struct {
			Plain reservedNotString  `forbid:"x" json:"plain"`
			Ptr   *reservedNotString `forbid:"x" json:"ptr"`
		}

		interp := boundInterp(func(c *jsonschema.Constraints) error {
			c.Forbid("x")

			return nil
		})

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTagInterpreter("forbid", interp), typeSchema())
		require.NoError(t, err)

		plain := s.Properties["plain"]
		require.NotNil(t, plain)
		require.NotNil(t, plain.Not, "the type not survives the field forbid")
		assert.Equal(t, []any{"reserved", "x"}, plain.Not.Enum,
			"the field forbid folds into the type not's escalation")

		for _, instance := range []map[string]any{
			{"plain": "ok", "ptr": "ok"},
			{"plain": "ok", "ptr": nil},
		} {
			require.NoError(t, jsonschema.Validate(t.Context(), s, instance),
				"instance %v must satisfy both forbids on both occurrences", instance)
		}

		for _, instance := range []map[string]any{
			{"plain": "reserved", "ptr": "ok"},
			{"plain": "ok", "ptr": "reserved"},
			{"plain": "x", "ptr": "ok"},
			{"plain": "ok", "ptr": "x"},
		} {
			require.Error(t, jsonschema.Validate(t.Context(), s, instance),
				"instance %v must fail: the type not and the field forbid both hold", instance)
		}
	})

	t.Run("forbid schema conjoins under allOf", func(t *testing.T) {
		t.Parallel()

		type doc struct {
			Plain reservedNotString `forbid:"x" json:"plain"`
		}

		three := 3
		interp := boundInterp(func(c *jsonschema.Constraints) error {
			c.ForbidSchema(&jsonschema.Schema{MinLength: &three})

			return nil
		})

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTagInterpreter("forbid", interp), typeSchema())
		require.NoError(t, err)

		require.NoError(t, jsonschema.Validate(t.Context(), s, map[string]any{"plain": "ab"}),
			"a value neither forbid matches validates")
		require.Error(t, jsonschema.Validate(t.Context(), s, map[string]any{"plain": "reserved"}),
			"the type not still holds beside the forbidden schema")
		require.Error(t, jsonschema.Validate(t.Context(), s, map[string]any{"plain": "abc"}),
			"the forbidden schema still holds beside the type not")
	})
}
