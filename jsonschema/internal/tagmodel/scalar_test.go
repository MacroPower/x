package tagmodel_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// level is a numeric type that marshals itself as text, the shape that makes the
// coercion round-trip observable: its serialized form is neither the number nor
// the number's decimal spelling, so a scalar that skipped the round-trip would
// produce a constraint no instance the field emits could ever satisfy.
type level int

// MarshalJSON writes the level as "L<n>".
func (l level) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, "%q", fmt.Sprintf("L%d", int(l))), nil
}

// stringSchema is the type-derived base a coerced or text-marshaled field
// carries: the generator emits a string schema for it, which is what makes the
// Go kind and the instance shape disagree.
func stringSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "string"} }

func TestShapeParseScalarNative(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ  reflect.Type
		base *jsonschema.Schema
		lit  string
		want any
		form tagmodel.Form
	}{
		"string": {
			typ: reflect.TypeFor[string](), lit: "hello",
			form: tagmodel.FormString, want: "hello",
		},
		"bool": {
			typ: reflect.TypeFor[bool](), lit: "true",
			form: tagmodel.FormBool, want: true,
		},
		"int widens to int64": {
			typ: reflect.TypeFor[int](), lit: "5",
			form: tagmodel.FormNumber, want: int64(5),
		},
		"uint widens to uint64": {
			typ: reflect.TypeFor[uint8](), lit: "5",
			form: tagmodel.FormNumber, want: uint64(5),
		},
		"float": {
			typ: reflect.TypeFor[float64](), lit: "0.5",
			form: tagmodel.FormNumber, want: 0.5,
		},
		"a string kind under a number schema is a number": {
			// A verbatim or overridden payload makes the instance a number even
			// though the Go kind is a string, so the numeric keywords are the
			// ones that constrain it. The Go kind supplies no integer width to
			// parse at, so the value takes the widest numeric form.
			typ:  reflect.TypeFor[string](),
			base: &jsonschema.Schema{Type: "integer"},
			lit:  "7", form: tagmodel.FormNumber, want: 7.0,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sh := tagmodel.ShapeOf(tc.typ, tc.base)
			require.Equal(t, tc.form, sh.Form)

			got, err := sh.ParseScalar(tc.lit, tagmodel.Policy{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestShapeParseScalarCoerced(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ  reflect.Type
		lit  string
		want string
		form tagmodel.Form
	}{
		"a plain coerced int emits its decimal text": {
			typ: reflect.TypeFor[int](), lit: "5",
			form: tagmodel.FormCoercedNumber, want: "5",
		},
		"a coerced float canonicalizes a float spelling": {
			// The go-playground parser accepts 5.0 where encoding/json emits 5, so the
			// round-trip is what makes the constraint match the instance.
			typ: reflect.TypeFor[float64](), lit: "5.0",
			form: tagmodel.FormCoercedNumber, want: "5",
		},
		"a coerced float canonicalizes an exponent spelling": {
			typ: reflect.TypeFor[float64](), lit: "1e2",
			form: tagmodel.FormCoercedNumber, want: "100",
		},
		"a coerced int canonicalizes a signed padded spelling": {
			// An integer kind parses in base 10, so it takes the spellings
			// strconv accepts there -- a leading sign, leading zeros -- and not
			// the float ones.
			typ: reflect.TypeFor[int](), lit: "+05",
			form: tagmodel.FormCoercedNumber, want: "5",
		},
		"a coerced bool emits its literal": {
			typ: reflect.TypeFor[bool](), lit: "false",
			form: tagmodel.FormCoercedBool, want: "false",
		},
		"a text-marshaling numeric type emits its own form": {
			// The round-trip converts the parsed number back to the field's own
			// Go type before marshaling, so the constraint carries what the type
			// writes rather than what the number looks like.
			typ: reflect.TypeFor[level](), lit: "3",
			form: tagmodel.FormCoercedNumber, want: "L3",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sh := tagmodel.ShapeOf(tc.typ, stringSchema())
			require.Equal(t, tc.form, sh.Form)

			got, err := sh.ParseScalar(tc.lit, tagmodel.Policy{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestShapeParseScalarRangeChecked confirms a coerced scalar keeps the range
// check the native path applies: parsing happens at the real Go kind, so a value
// the field could never hold is an error rather than a constraint pinned to text
// the field never writes.
func TestShapeParseScalarRangeChecked(t *testing.T) {
	t.Parallel()

	for name, base := range map[string]*jsonschema.Schema{
		"native":  nil,
		"coerced": stringSchema(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sh := tagmodel.ShapeOf(reflect.TypeFor[int8](), base)

			_, err := sh.ParseScalar("200", tagmodel.Policy{})
			require.Error(t, err, "200 does not fit an int8")
			assert.Contains(t, err.Error(), "out of range")
		})
	}
}

// TestShapeParseScalarNullPolicy pins the one scalar divergence the dialects
// keep: the jsonschema tag spells JSON null as the literal "null" on a nullable
// shape, while the validate tag has no null literal at all, so there the same
// four characters are an ordinary string.
func TestShapeParseScalarNullPolicy(t *testing.T) {
	t.Parallel()

	nullable := tagmodel.ShapeOf(reflect.TypeFor[*string](), stringSchema())

	t.Run("a dialect with a null literal yields JSON null", func(t *testing.T) {
		t.Parallel()

		got, err := nullable.ParseScalar("null", tagmodel.Policy{AllowNullScalar: true})
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("a null literal on a non-nullable shape is an error", func(t *testing.T) {
		t.Parallel()

		plain := tagmodel.ShapeOf(reflect.TypeFor[string](), stringSchema())

		_, err := plain.ParseScalar("null", tagmodel.Policy{AllowNullScalar: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot assign null")
	})

	t.Run("a dialect without a null literal yields the string", func(t *testing.T) {
		t.Parallel()

		got, err := nullable.ParseScalar("null", tagmodel.Policy{})
		require.NoError(t, err)
		assert.Equal(t, "null", got)
	})
}

// TestShapeZeroLiteralRoutesThroughParseScalar is the direct guard on the bug
// class a hardcoded zero produced: the non-zero assertion must forbid the text
// the field actually emits, which for a text-marshaling type is neither "0" nor
// any spelling of the number.
func TestShapeZeroLiteralRoutesThroughParseScalar(t *testing.T) {
	t.Parallel()

	sh := tagmodel.ShapeOf(reflect.TypeFor[level](), stringSchema())

	got, err := sh.ParseScalar(sh.ZeroLiteralForTest(), tagmodel.Policy{})
	require.NoError(t, err)
	assert.Equal(t, "L0", got,
		"the forbidden zero is what the type serializes, not a hardcoded \"0\"")
}

// TestShapeParseScalarUnsupportedForms confirms a shape with no tag-spellable
// value reports rather than coercing one into existence.
func TestShapeParseScalarUnsupportedForms(t *testing.T) {
	t.Parallel()

	for name, typ := range map[string]reflect.Type{
		"array":  reflect.TypeFor[[]string](),
		"object": reflect.TypeFor[map[string]int](),
		"opaque": reflect.TypeFor[struct{ A int }](),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tagmodel.ShapeOf(typ, nil).ParseScalar("x", tagmodel.Policy{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot assign scalar value")
		})
	}
}
