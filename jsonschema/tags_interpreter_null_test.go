package jsonschema_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// interpNullRecursive is the self-referential type the interpreter
// null-literal tests configure. The field carries no jsonschema tag, so the
// canvas scan reports the literal. A recorded tag key takes precedence over the
// canvas, and this type spells none.
type interpNullRecursive struct {
	Next *interpNullRecursive `json:"next" mytag:"x"`
}

// interpNullElement is the self-referential type whose recursion sits on a
// slice element. A []*T element is a pointer occurrence, so it reads the same
// early null answer the field does. A []T element is a non-pointer occurrence
// whose answer is already false and would exercise the wrong window.
type interpNullElement struct {
	Kids []*interpNullElement `json:"kids" mytag:"x"`
}

// interpNullMapElement is the map-valued sibling of interpNullElement. A map
// value and a slice element share one node field, so the pair pins that the
// origin walk reaches both.
type interpNullMapElement struct {
	Kids map[string]*interpNullMapElement `json:"kids" mytag:"x"`
}

// interpNullTupleElement is the fixed-array sibling. A tuple keeps one node per
// position, which the origin walk descends separately from a slice element.
type interpNullTupleElement struct {
	Kids [2]*interpNullTupleElement `json:"kids" mytag:"x"`
}

// interpNullBothWriters carries a jsonschema tag null literal beside the
// interpreter's keyword, so both field-level writers commit a null to one
// field.
type interpNullBothWriters struct {
	Next *interpNullBothWriters `json:"next" jsonschema:"default=null" mytag:"x"`
}

// interpNullOther is the type the wider-gate control gives a NullForbidden
// stance. Nothing references it from inside its own definition, so its stance
// is final before an interpreter reads the decision.
type interpNullOther struct {
	X string `json:"x"`
}

// interpNullHolder references interpNullOther through a pointer field, the
// occurrence whose null admission the stance on interpNullOther refuses.
type interpNullHolder struct {
	Next *interpNullOther `json:"next" mytag:"x"`
}

// interpNullRequired is the shape TestValidateRequiredOnARecursiveStancedType
// generates. The omitempty is load-bearing. Without it the json tag alone adds
// the required entry, and the assertion could not tell which writer added it.
type interpNullRequired struct {
	Next *interpNullRequired `json:"next,omitempty" validate:"required"`
}

// nullCanvasWrite is one row of the canvas-writer table: the function spelling
// a JSON null onto a canvas, and the keyword the resulting report must name.
type nullCanvasWrite struct {
	write   func(canvas *jsonschema.Schema)
	keyword string
}

// baseNullCanvasWrites returns one row per canvas keyword a tag interpreter
// spells a JSON null on. Default takes the literal as raw JSON, and the const,
// enum, and examples rows each carry an untyped nil. The scan in
// canvasNullLiteral reads exactly these four keywords, so a test covering it
// ranges this set rather than [nullCanvasWrites].
func baseNullCanvasWrites() map[string]nullCanvasWrite {
	return map[string]nullCanvasWrite{
		"default": {
			keyword: "default",
			write: func(canvas *jsonschema.Schema) {
				canvas.Default = jsontext.Value("null")
			},
		},
		"const": {
			keyword: "const",
			write: func(canvas *jsonschema.Schema) {
				var null any

				canvas.Const = &null
			},
		},
		"enum": {
			keyword: "enum",
			write: func(canvas *jsonschema.Schema) {
				canvas.Enum = []any{nil}
			},
		},
		"examples": {
			keyword: "examples",
			write: func(canvas *jsonschema.Schema) {
				canvas.Examples = []any{nil}
			},
		},
	}
}

// nullCanvasWrites returns the keyword rows of [baseNullCanvasWrites] plus one
// row per Go value form isJSONNull judges rather than recognizes by identity: a
// typed nil, and a [jsontext.Value] holding the literal, the same spelling the
// default row uses.
func nullCanvasWrites() map[string]nullCanvasWrite {
	writes := baseNullCanvasWrites()

	maps.Copy(writes, map[string]nullCanvasWrite{
		"typed nil const": {
			keyword: "const",
			write: func(canvas *jsonschema.Schema) {
				var null any = (*string)(nil)

				canvas.Const = &null
			},
		},
		"typed nil enum member": {
			keyword: "enum",
			write: func(canvas *jsonschema.Schema) {
				// A nil pointer, not a nil slice: encoding/json/v2 writes a nil
				// slice as [], so only pointer and interface nils spell null.
				canvas.Enum = []any{(*int)(nil)}
			},
		},
		"raw const": {
			keyword: "const",
			write: func(canvas *jsonschema.Schema) {
				var null any = jsontext.Value("null")

				canvas.Const = &null
			},
		},
		"raw enum member": {
			keyword: "enum",
			write: func(canvas *jsonschema.Schema) {
				canvas.Enum = []any{jsontext.Value("null")}
			},
		},
	})

	return writes
}

// TestInterpreterNullLiteralOnARecursiveStancedType pins the re-check over the
// second field-level writer. A field referencing the type it belongs to
// resolves against a $defs entry still being built, so the stance the extender
// records lands after the interpreter has read the decision as admitting null
// and written a literal against it. The generator scans the authored canvas
// once every stance is final and reports the keyword the final decision
// refuses.
//
// The interpreter records what it observed rather than asserting on it. A
// failed assertion inside the interpreter would abandon the generator's own
// call stack.
func TestInterpreterNullLiteralOnARecursiveStancedType(t *testing.T) {
	t.Parallel()

	for name, tc := range nullCanvasWrites() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var nullable bool

			interp := jsonschema.TagInterpreterFunc(
				func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
					nullable = field.Shape().Nullable
					tc.write(field.Canvas)

					return nil
				},
			)

			_, err := jsonschema.GenerateFor[interpNullRecursive](
				t.Context(),
				jsonschema.WithTagInterpreter("mytag", interp),
				forbidNullStance[interpNullRecursive](),
			)

			assert.True(t, nullable, "the interpreter reads the early null answer")
			require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
			require.ErrorContains(t, err,
				`jsonschema_test.interpNullRecursive field "next": `+
					`authored canvas: keyword "`+tc.keyword+`"`)
		})
	}
}

// TestInterpreterNullLiteralOnARecursiveElement pins the same re-check where
// the literal sits on an element rather than on the field. ElementContexts
// hands out the element's own node, which reads the same early answer the field
// does. The element therefore carries the field's origin, and the report names
// that field and marks the position as an element.
//
// It ranges the keyword rows alone. The extra value forms
// [nullCanvasWrites] adds differ only in what isJSONNull judges, which
// [TestInterpreterNullLiteralOnARecursiveStancedType] pins through the
// identical canvasNullLiteral call.
func TestInterpreterNullLiteralOnARecursiveElement(t *testing.T) {
	t.Parallel()

	for name, tc := range baseNullCanvasWrites() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				elemCount int
				nullable  bool
			)

			interp := jsonschema.TagInterpreterFunc(
				func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
					elems := field.ElementContexts()

					elemCount = len(elems)
					if elemCount != 1 {
						return nil
					}

					nullable = elems[0].Shape().Nullable
					tc.write(elems[0].Canvas)

					return nil
				},
			)

			_, err := jsonschema.GenerateFor[interpNullElement](
				t.Context(),
				jsonschema.WithTagInterpreter("mytag", interp),
				forbidNullStance[interpNullElement](),
			)

			require.Equal(t, 1, elemCount)
			assert.True(t, nullable, "the element reads the early null answer")
			require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
			require.ErrorContains(t, err,
				`jsonschema_test.interpNullElement field "kids": element: `+
					`authored canvas: keyword "`+tc.keyword+`"`)
		})
	}
}

// TestInterpreterNullLiteralOnMapAndTupleElements covers the two element
// positions TestInterpreterNullLiteralOnARecursiveElement does not. A map value
// rides the same node field a slice element does, while a tuple keeps one node
// per position, so together they exercise both branches of the origin walk.
func TestInterpreterNullLiteralOnMapAndTupleElements(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func(opt jsonschema.GenerateOption) error
		parent   string
	}{
		"map": {
			generate: func(opt jsonschema.GenerateOption) error {
				_, err := jsonschema.GenerateFor[interpNullMapElement](t.Context(),
					opt, forbidNullStance[interpNullMapElement]())

				return err
			},
			parent: "jsonschema_test.interpNullMapElement",
		},
		"tuple": {
			generate: func(opt jsonschema.GenerateOption) error {
				_, err := jsonschema.GenerateFor[interpNullTupleElement](t.Context(),
					opt, forbidNullStance[interpNullTupleElement]())

				return err
			},
			parent: "jsonschema_test.interpNullTupleElement",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			interp := jsonschema.TagInterpreterFunc(
				func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
					for _, elem := range field.ElementContexts() {
						elem.Canvas.Default = jsontext.Value("null")
					}

					return nil
				},
			)

			err := tc.generate(jsonschema.WithTagInterpreter("mytag", interp))
			require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
			require.ErrorContains(t, err,
				tc.parent+` field "kids": element: authored canvas: keyword "default"`)
		})
	}
}

// TestInterpreterNullLiteralYieldsToTheTagKey pins the precedence between the
// two writers. When a field's tag took a null literal and an interpreter wrote
// another onto its canvas, the pass reports the tag key. That is the fault the
// struct tag spells, so the report names the writer the author can act on.
func TestInterpreterNullLiteralYieldsToTheTagKey(t *testing.T) {
	t.Parallel()

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			field.Canvas.Enum = []any{nil}

			return nil
		},
	)

	_, err := jsonschema.GenerateFor[interpNullBothWriters](
		t.Context(),
		jsonschema.WithTagInterpreter("mytag", interp),
		forbidNullStance[interpNullBothWriters](),
	)

	require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
	require.ErrorContains(t, err,
		`jsonschema_test.interpNullBothWriters field "next": `+
			`jsonschema tag: key "default"`)
	require.NotContains(t, err.Error(), "authored canvas")
}

// TestConstraintsNullLiteralOnARecursiveStancedType pins that the shared
// model's own value setters reach the scan too. SetConst and SetEnum compose a
// const or an enum on the canvas without consulting the constraint matrix, so a
// null passed to either lands in the same keyword a direct canvas write would
// use.
func TestConstraintsNullLiteralOnARecursiveStancedType(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		set     func(c *jsonschema.Constraints) error
		keyword string
	}{
		"const": {
			keyword: "const",
			set:     func(c *jsonschema.Constraints) error { return c.SetConst(nil) },
		},
		"enum": {
			keyword: "enum",
			set: func(c *jsonschema.Constraints) error {
				return c.SetEnum([]any{nil})
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var setErr error

			interp := jsonschema.TagInterpreterFunc(
				func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
					setErr = tc.set(field.Constraints())

					return nil
				},
			)

			_, err := jsonschema.GenerateFor[interpNullRecursive](
				t.Context(),
				jsonschema.WithTagInterpreter("mytag", interp),
				forbidNullStance[interpNullRecursive](),
			)

			require.NoError(t, setErr, "the setter accepts the null")
			require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
			require.ErrorContains(t, err,
				`jsonschema_test.interpNullRecursive field "next": `+
					`authored canvas: keyword "`+tc.keyword+`"`)
		})
	}
}

// interpNullPanicMarshaler marshals by panicking, the shape that reaches the
// scan's own marshal through [json.Marshaler].
type interpNullPanicMarshaler struct{}

// MarshalJSON panics, standing in for a third-party marshaler with a bug.
func (interpNullPanicMarshaler) MarshalJSON() ([]byte, error) {
	panic("marshal")
}

// TestInterpreterPanickingMarshalerOnARecursiveStancedType pins that the scan
// survives the marshaler it consults. Deciding whether a canvas value is a null
// runs that value's own MarshalJSON, so a third-party marshaler with a bug
// would otherwise panic out of a call that reports through errors alone. The
// value is not a null, so generation carries it through to the caller's
// marshal, where the panic is the caller's to see.
func TestInterpreterPanickingMarshalerOnARecursiveStancedType(t *testing.T) {
	t.Parallel()

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			var value any = interpNullPanicMarshaler{}

			field.Canvas.Const = &value

			return nil
		},
	)

	require.NotPanics(t, func() {
		_, err := jsonschema.GenerateFor[interpNullRecursive](
			t.Context(),
			jsonschema.WithTagInterpreter("mytag", interp),
			forbidNullStance[interpNullRecursive](),
		)
		require.NoError(t, err)
	})
}

// TestInterpreterNullLiteralOnARecursiveType is the control the re-check must
// leave alone. The same interpreter over the same self-referential type with no
// stance recorded for it keeps the null the occurrence admits, and the literal
// renders on the wrapper beside the reference.
func TestInterpreterNullLiteralOnARecursiveType(t *testing.T) {
	t.Parallel()

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			field.Canvas.Default = jsontext.Value("null")

			return nil
		},
	)

	s, err := jsonschema.GenerateFor[interpNullRecursive](
		t.Context(),
		jsonschema.WithTagInterpreter("mytag", interp),
	)
	require.NoError(t, err)

	def := s.Defs["interpNullRecursive"]
	require.NotNil(t, def)

	got, err := json.Marshal(def.Properties["next"])
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"default":null,"anyOf":[{"$ref":"#/$defs/interpNullRecursive"},`+
			`{"type":"null"}]}`, string(got))
}

// TestInterpreterNullLiteralOnANonRecursiveStancedType pins that the scan
// covers the whole reference window rather than the withdrawn answer alone.
// The stance on interpNullOther is final before the interpreter runs, so the
// interpreter reads the decision as refusing null and writes the literal
// anyway. The literal is wrong either way, so the re-check reports it.
func TestInterpreterNullLiteralOnANonRecursiveStancedType(t *testing.T) {
	t.Parallel()

	var nullable bool

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			nullable = field.Shape().Nullable
			field.Canvas.Default = jsontext.Value("null")

			return nil
		},
	)

	_, err := jsonschema.GenerateFor[interpNullHolder](
		t.Context(),
		jsonschema.WithTagInterpreter("mytag", interp),
		forbidNullStance[interpNullOther](),
	)

	assert.False(t, nullable, "the stance is final before the interpreter runs")
	require.ErrorIs(t, err, tagmodel.ErrNullNotAdmitted)
	require.ErrorContains(t, err,
		`jsonschema_test.interpNullHolder field "next": `+
			`authored canvas: keyword "default"`)
}

// TestInterpreterNullForbidOnARecursiveStancedType pins the other half of the
// rule. Forbidding null writes under not rather than into a value keyword, so
// the scan leaves it alone and the forbid renders beside the reference. The
// forbid asserts nothing the reference does not assert already.
func TestInterpreterNullForbidOnARecursiveStancedType(t *testing.T) {
	t.Parallel()

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			field.Constraints().Forbid(nil)

			return nil
		},
	)

	s, err := jsonschema.GenerateFor[interpNullRecursive](
		t.Context(),
		jsonschema.WithTagInterpreter("mytag", interp),
		forbidNullStance[interpNullRecursive](),
	)
	require.NoError(t, err)

	def := s.Defs["interpNullRecursive"]
	require.NotNil(t, def)

	got, err := json.Marshal(def.Properties["next"])
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"$ref":"#/$defs/interpNullRecursive","not":{"const":null}}`, string(got))
}

// TestValidateRequiredOnARecursiveStancedType pins that the built-in dialect
// cannot reach the canvas scan at all. The constraint matrix ignores a non-zero
// rule on a referenced definition, so required on a self-referential pointer
// adds the required entry and forbids nothing, whichever stance the type
// carries.
func TestValidateRequiredOnARecursiveStancedType(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		stance []jsonschema.GenerateOption
	}{
		"no stance": {},
		"null forbidden": {
			stance: []jsonschema.GenerateOption{
				forbidNullStance[interpNullRequired](),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := []jsonschema.GenerateOption{
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
			}

			s, err := jsonschema.GenerateFor[interpNullRequired](
				t.Context(), append(opts, tc.stance...)...,
			)
			require.NoError(t, err)

			def := s.Defs["interpNullRequired"]
			require.NotNil(t, def)
			assert.Equal(t, []string{"next"}, def.Required)

			got, err := json.Marshal(def.Properties["next"])
			require.NoError(t, err)
			assert.NotContains(t, string(got), `"not"`)
		})
	}
}
