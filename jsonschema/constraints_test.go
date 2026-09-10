package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"math"
	"reflect"
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

			require.NoError(t, c.Forbid("y"))

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

	t.Run("enum intersects with a type-set enum", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			Name string `json:"name" pin:"x"`
		}

		var conflict error

		interp := boundInterp(func(c *jsonschema.Constraints) error {
			err := c.SetEnum([]any{"b", "a", "x"})
			if err != nil {
				return fmt.Errorf("first enumeration: %w", err)
			}

			conflict = c.SetEnum([]any{"x"})

			return nil
		})

		s, err := jsonschema.GenerateFor[Payload](t.Context(),
			jsonschema.WithTagInterpreter("pin", interp),
			jsonschema.WithTypeSchemaFor[string](jsonschema.TypeSchema{
				Value: &jsonschema.Schema{Type: "string", Enum: []any{"a", "b", "c"}},
			}),
		)
		require.NoError(t, err)
		assert.Equal(t, []any{"a", "b"}, s.Properties["name"].Enum,
			"a second enumeration narrows the type's own, in the type's order")
		require.ErrorIs(t, conflict, jsonschema.ErrConstraintConflict,
			"an enumeration sharing no value with the one in force is a conflict")
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
		okErr = c.Apply(jsonschema.OpFloorIncl, jsonschema.AxisNumeric, "10")
		bigErr = c.Apply(jsonschema.OpCeilIncl, jsonschema.AxisNumeric, "9007199254740993")

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
		require.NoError(t, c.Apply(jsonschema.OpFloorIncl, jsonschema.AxisNumeric, "-200"))
		// A ceiling tighter than the kind ceiling is applied.
		require.NoError(t, c.Apply(jsonschema.OpCeilIncl, jsonschema.AxisNumeric, "50"))

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

// TestConstraintsFacadeCountIntersection confirms a count bound targets the
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
			require.NoError(t, c.Apply(jsonschema.OpFloorIncl, jsonschema.AxisAuto, "2"))
			require.NoError(t, c.Apply(jsonschema.OpCeilIncl, jsonschema.AxisAuto, "5"))

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
			return c.Apply(jsonschema.OpCeilExcl, jsonschema.AxisAuto, "0")
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
			// Naming the family rather than letting the shape choose is what
			// makes this an error: an auto axis on an int is a numeric bound,
			// which is a perfectly good thing to write.
			countErr = c.Apply(jsonschema.OpFloorIncl, jsonschema.AxisItems, "2")

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
		return c.Apply(jsonschema.OpFloorIncl, jsonschema.AxisNumeric, "15")
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
		return c.Apply(jsonschema.OpFloorIncl, jsonschema.AxisNumeric, "10")
	})
	hi := boundInterp(func(c *jsonschema.Constraints) error {
		return c.Apply(jsonschema.OpCeilIncl, jsonschema.AxisNumeric, "100")
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
			return c.Forbid("x")
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
			return c.ForbidSchema(&jsonschema.Schema{MinLength: &three})
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

// TestConstraintsForbidSchemaSparesNull pins that a forbidden subschema judges
// the value branch alone, so a nullable field's null passes it whichever
// order the forbids arrive in. A schema forbid used to take the free not slot,
// which the null split scopes to the wrapper, so a bare length range (one
// naming no type) matched null vacuously and the not rejected it, while the
// same forbid after a value forbid moved under allOf and let the null through.
func TestConstraintsForbidSchemaSparesNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		Alone *string `forbid:"x" json:"alone"`
		After *string `forbid:"x" json:"after"`
		Plain string  `forbid:"x" json:"plain"`
	}

	exact := &jsonschema.Schema{MinLength: new(3), MaxLength: new(3)}

	forbid := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			c := field.Constraints()
			if field.Name == "after" {
				require.NoError(t, c.Forbid("zzz"))
			}

			require.NoError(t, c.ForbidSchema(exact))

			return nil
		},
	)

	s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("forbid", forbid))
	require.NoError(t, err)

	require.NoError(t, jsonschema.Validate(t.Context(), s,
		map[string]any{"alone": nil, "after": nil, "plain": "ab"}),
		"a null the field's decision admits is never judged by a forbidden subschema")
	require.NoError(t, jsonschema.Validate(t.Context(), s,
		map[string]any{"alone": "ab", "after": "ab", "plain": "abcd"}),
		"a value outside the forbidden range passes on every occurrence")

	for _, name := range []string{"alone", "after", "plain"} {
		instance := map[string]any{"alone": "ab", "after": "ab", "plain": "ab"}
		instance[name] = "abc"

		require.Error(t, jsonschema.Validate(t.Context(), s, instance),
			"field %q must still reject the forbidden length", name)
	}
}

// TestConstraintsForbidSchemaSparesNullOnATypeList pins that a forbidden
// subschema judges the value branch alone on a nilable container too. Such a
// field's null render is a ["null", base] type list rather than an anyOf
// wrapper, and the allOf used to ride that list inline, where a forbidden
// subschema naming no type matched null vacuously and the not rejected it,
// while the same forbid on a *string kept null valid. The container takes
// the anyOf form with the allOf on the value branch.
func TestConstraintsForbidSchemaSparesNullOnATypeList(t *testing.T) {
	t.Parallel()

	type doc struct {
		Items *[]int           `forbid:"x" json:"items"`
		Names *map[string]bool `forbid:"x" json:"names"`
		Str   *string          `forbid:"x" json:"str"`
	}

	forbid := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			c := field.Constraints()

			switch field.Name {
			case "items":
				return c.ForbidSchema(&jsonschema.Schema{MaxItems: new(2)})
			case "names":
				return c.ForbidSchema(&jsonschema.Schema{MaxProperties: new(2)})
			default:
				return c.ForbidSchema(&jsonschema.Schema{MaxLength: new(2)})
			}
		},
	)

	s, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("forbid", forbid))
	require.NoError(t, err)

	require.NoError(t, jsonschema.Validate(t.Context(), s,
		map[string]any{"items": nil, "names": nil, "str": nil}),
		"a null the field's decision admits is never judged by a forbidden subschema")

	three := map[string]any{"a": true, "b": false, "c": true}

	require.NoError(t, jsonschema.Validate(t.Context(), s,
		map[string]any{"items": []any{1, 2, 3}, "names": three, "str": "abc"}),
		"a value outside the forbidden range passes on every container")

	for _, instance := range []map[string]any{
		{"items": []any{1, 2}, "names": three, "str": "abc"},
		{"items": []any{1, 2, 3}, "names": map[string]any{"a": true}, "str": "abc"},
		{"items": []any{1, 2, 3}, "names": three, "str": "ab"},
	} {
		require.Error(t, jsonschema.Validate(t.Context(), s, instance),
			"the forbidden subschema still holds on the value branch of %v", instance)
	}
}

// TestConstraintsFacadeNilCanvasWrites pins the facade's write boundary:
// every write on a facade with no canvas, a nil *Constraints, the zero
// Constraints, or one a caller-built context handed out without a Canvas, is
// ErrNilCanvas and leaves nothing behind, while the reads answer as nothing
// set.
func TestConstraintsFacadeNilCanvasWrites(t *testing.T) {
	t.Parallel()

	three := 3
	writes := map[string]func(c *jsonschema.Constraints) error{
		"Apply": func(c *jsonschema.Constraints) error {
			return c.Apply(jsonschema.OpFloorIncl, jsonschema.AxisAuto, "1")
		},
		"SetMultipleOf": func(c *jsonschema.Constraints) error { return c.SetMultipleOf(2) },
		"SetConst":      func(c *jsonschema.Constraints) error { return c.SetConst("x") },
		"SetEnum":       func(c *jsonschema.Constraints) error { return c.SetEnum([]any{"x"}) },
		"Forbid":        func(c *jsonschema.Constraints) error { return c.Forbid("x") },
		"ForbidSchema":  func(c *jsonschema.Constraints) error { return c.ForbidSchema(&jsonschema.Schema{MinLength: &three}) },
	}

	assert.Len(t, writes, reflect.TypeFor[*jsonschema.Constraints]().NumMethod()-2,
		"every write on the facade takes a row here; only Const and Enum are reads")

	facades := map[string]*jsonschema.Constraints{
		"nil facade":        nil,
		"zero facade":       {},
		"canvas-less field": jsonschema.FieldContext{Type: reflect.TypeFor[string]()}.Constraints(),
	}

	for facadeName, c := range facades {
		for writeName, write := range writes {
			t.Run(facadeName+" "+writeName, func(t *testing.T) {
				t.Parallel()

				require.ErrorIs(t, write(c), jsonschema.ErrNilCanvas)
			})
		}

		t.Run(facadeName+" reads", func(t *testing.T) {
			t.Parallel()

			value, ok := c.Const()
			assert.Nil(t, value)
			assert.False(t, ok)

			members, ok := c.Enum()
			assert.Nil(t, members)
			assert.False(t, ok)
		})
	}
}

// TestConstraintsFacadeInvalidRule pins that a rule the model has no row for
// is ErrInvalidRule and leaves no trace: an operation or axis outside the
// table, a parameter count the operation does not take (a missing single
// value would pin the empty string, an extra one would be dropped, an empty
// enumeration would forbid every instance), a non-boolean uniqueness literal,
// and a nil subschema to forbid.
func TestConstraintsFacadeInvalidRule(t *testing.T) {
	t.Parallel()

	tests := map[string]func(c *jsonschema.Constraints) error{
		"unset op": func(c *jsonschema.Constraints) error { return c.Apply(jsonschema.Op(0), jsonschema.AxisAuto, "1") },
		"op past the table": func(c *jsonschema.Constraints) error {
			return c.Apply(jsonschema.Op(200), jsonschema.AxisAuto, "1")
		},
		"axis past the table": func(c *jsonschema.Constraints) error {
			return c.Apply(jsonschema.OpFloorIncl, jsonschema.Axis(9), "1")
		},
		"missing value": func(c *jsonschema.Constraints) error { return c.Apply(jsonschema.OpNotEqual, jsonschema.AxisAuto) },
		"extra value": func(c *jsonschema.Constraints) error {
			return c.Apply(jsonschema.OpEqual, jsonschema.AxisAuto, "a", "b")
		},
		"empty enumeration": func(c *jsonschema.Constraints) error { return c.Apply(jsonschema.OpOneOf, jsonschema.AxisAuto) },
		// SetEnum used to accept an empty list, leaving a non-nil empty Enum the
		// compiled struct rejected every instance with while its JSON form,
		// which omits an empty enum, accepted them.
		"empty enumeration through SetEnum": func(c *jsonschema.Constraints) error { return c.SetEnum([]any{}) },
		"unique non-boolean": func(c *jsonschema.Constraints) error {
			return c.Apply(jsonschema.OpUnique, jsonschema.AxisAuto, "yes")
		},
		"nil forbidden schema": func(c *jsonschema.Constraints) error { return c.ForbidSchema(nil) },
	}

	for name, apply := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fc := jsonschema.FieldContext{
				Type:   reflect.TypeFor[[]string](),
				Canvas: &jsonschema.Schema{},
				Base:   &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
			}

			require.ErrorIs(t, apply(fc.Constraints()), jsonschema.ErrInvalidRule)
			assert.Equal(t, &jsonschema.Schema{}, fc.Canvas, "the misapplied rule must leave no trace")
		})
	}
}

// TestShapeOfUnresolvedReference pins that a bare $ref base is unresolved
// wherever nothing can read the definition: the public ShapeOf, and a
// caller-built context with no backing node. A rule on it is the shape
// refusal, naming the unreadable definition.
func TestShapeOfUnresolvedReference(t *testing.T) {
	t.Parallel()

	type object struct{}

	base := &jsonschema.Schema{Ref: "#/$defs/object"}

	assert.Equal(t, jsonschema.FormUnresolvedRef, jsonschema.ShapeOf(reflect.TypeFor[object](), base).Form)

	fc := jsonschema.FieldContext{Type: reflect.TypeFor[object](), Base: base, Canvas: &jsonschema.Schema{}}
	assert.Equal(t, jsonschema.FormUnresolvedRef, fc.Shape().Form)

	err := fc.Constraints().Apply(jsonschema.OpFloorIncl, jsonschema.AxisProperties, "1")
	require.ErrorIs(t, err, jsonschema.ErrConstraintUnsupported)
	require.ErrorContains(t, err, "not readable here")
}

// TestConstraintsMultipleOfComposes pins that an inferred multipleOf never
// loosens one already in force. An interpreter's divisor intersects with the
// jsonschema tag's, or with the one the field's type declares, to their least
// common multiple, and two interpreters compose the same whichever order they
// run in. A tag divisor still replaces the type's, as a named format does.
func TestConstraintsMultipleOfComposes(t *testing.T) {
	t.Parallel()

	three := boundInterp(func(c *jsonschema.Constraints) error { return c.SetMultipleOf(3) })
	ten := boundInterp(func(c *jsonschema.Constraints) error { return c.SetMultipleOf(10) })

	t.Run("interpreter over tag", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			V int `div:"x" json:"v" jsonschema:"multipleOf=10"`
		}

		s, err := jsonschema.GenerateFor[Payload](t.Context(), jsonschema.WithTagInterpreter("div", three))
		require.NoError(t, err)
		require.NotNil(t, s.Properties["v"].MultipleOf)
		assert.InDelta(t, 30, *s.Properties["v"].MultipleOf, 0)
	})

	t.Run("interpreter over type", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			V int `div:"x" json:"v"`
		}

		four := 4.0

		s, err := jsonschema.GenerateFor[Payload](t.Context(),
			jsonschema.WithTagInterpreter("div", three),
			jsonschema.WithTypeSchemaFor[int](jsonschema.TypeSchema{
				Value: &jsonschema.Schema{Type: "integer", MultipleOf: &four},
			}))
		require.NoError(t, err)
		require.NotNil(t, s.Properties["v"].MultipleOf)
		assert.InDelta(t, 12, *s.Properties["v"].MultipleOf, 0)
	})

	t.Run("two interpreters in either order", func(t *testing.T) {
		t.Parallel()

		type Payload struct {
			V int `a:"x" b:"x" json:"v"`
		}

		for name, opts := range map[string][]jsonschema.GenerateOption{
			"three then ten": {jsonschema.WithTagInterpreter("a", three), jsonschema.WithTagInterpreter("b", ten)},
			"ten then three": {jsonschema.WithTagInterpreter("a", ten), jsonschema.WithTagInterpreter("b", three)},
		} {
			s, err := jsonschema.GenerateFor[Payload](t.Context(), opts...)
			require.NoError(t, err, name)
			require.NotNil(t, s.Properties["v"].MultipleOf, name)
			assert.InDelta(t, 30, *s.Properties["v"].MultipleOf, 0, name)
		}
	})
}

// infinityWriter is a tag interpreter that writes an infinite bound straight
// onto the canvas, past the facade's finite check.
type infinityWriter struct{}

func (infinityWriter) Interpret(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
	field.Canvas.Minimum = new(math.Inf(1))

	return nil
}

// TestInterpreterCanvasNonFiniteBoundRefused pins that a non-finite bound an
// interpreter writes on the canvas is refused as a type-level hook's is. The
// bound algebra reads such a value as no bound, so the schema would silently
// carry none where the interpreter meant one.
func TestInterpreterCanvasNonFiniteBoundRefused(t *testing.T) {
	t.Parallel()

	type doc struct {
		N float64 `inf:"x" json:"n"`
	}

	_, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("inf", infinityWriter{}))
	require.ErrorIs(t, err, jsonschema.ErrNonFiniteBound)
	assert.ErrorContains(t, err, `tag interpreter "inf" declares minimum +Inf`)
}

// TestInterpreterElementCanvasNonFiniteBoundRefused pins ErrNonFiniteBound
// against the element canvases ElementContexts hands out, at any depth. The
// check used to read the field's own canvas alone, so an interpreter writing
// an infinity or a NaN on an element canvas generated with no error and the
// bound algebra dropped the bound, the outcome the sentinel exists to
// prevent.
func TestInterpreterElementCanvasNonFiniteBoundRefused(t *testing.T) {
	t.Parallel()

	type doc struct {
		Items  []float64   `inf:"x" json:"items"`
		Nested [][]float64 `inf:"x" json:"nested"`
	}

	tests := map[string]struct {
		write func(canvas *jsonschema.Schema)
		want  string
	}{
		"infinity on the element": {
			write: func(canvas *jsonschema.Schema) { canvas.Minimum = new(math.Inf(1)) },
			want:  `declares minimum +Inf`,
		},
		"NaN on the element": {
			write: func(canvas *jsonschema.Schema) { canvas.Maximum = new(math.NaN()) },
			want:  `declares maximum NaN`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			interp := jsonschema.TagInterpreterFunc(
				func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
					elems := field.ElementContexts()
					require.Len(t, elems, 1)

					// Descend to the innermost element so the nested field
					// writes two levels down.
					for inner := elems[0].ElementContexts(); len(inner) == 1; inner = inner[0].ElementContexts() {
						elems = inner
					}

					tc.write(elems[0].Canvas)

					return nil
				},
			)

			_, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("inf", interp))
			require.ErrorIs(t, err, jsonschema.ErrNonFiniteBound)
			assert.ErrorContains(t, err, `tag interpreter "inf" `+tc.want)
		})
	}
}
