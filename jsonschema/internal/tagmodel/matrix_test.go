package tagmodel_test

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// TestMatrixTotal is the structural exhaustiveness guard, the counterpart of the
// schemafield table's upstream check. The package's own init panics on an
// unfilled cell, so reaching this test at all proves the table is total; the
// assertions state the guarantee where a reader looks for it and report a
// readable failure rather than a panic if the walk is ever relaxed.
func TestMatrixTotal(t *testing.T) {
	t.Parallel()

	dump := tagmodel.DumpMatrix()

	assert.NotContains(t, dump, "UNSET",
		"every operation/shape pair carries a decision")

	for line := range strings.SplitSeq(strings.TrimSpace(dump), "\n") {
		pair, verdict, found := strings.Cut(line, " -> ")
		require.True(t, found, "every dump line names a pair and a verdict: %q", line)

		status, reason, hasReason := strings.Cut(verdict, ": ")
		if status == "ignore" || status == "reject" {
			assert.True(t, hasReason && reason != "",
				"%s states why it is a %s", pair, status)
		}
	}
}

// TestMatrixGolden pins the whole table, so a deliberate cell change is a
// reviewable diff in this file and an accidental one is a failure. The dump is
// sorted, so a new operation or shape shows up as inserted lines rather than
// reshuffling everything after it.
func TestMatrixGolden(t *testing.T) {
	t.Parallel()

	want := stringtest.Margin(`
		|content encoding / array -> reject: content encoding is not supported on a array
		|content encoding / base64 byte string -> apply
		|content encoding / boolean -> reject: content encoding is not supported on a boolean
		|content encoding / number -> reject: content encoding is not supported on a number
		|content encoding / object -> reject: content encoding is not supported on a object
		|content encoding / opaque value -> reject: content encoding is not supported on a opaque value
		|content encoding / raw byte slice -> reject: content encoding is not supported on a raw byte slice
		|content encoding / string -> apply
		|content encoding / string-coerced boolean -> apply
		|content encoding / string-coerced number -> apply
		|content encoding / text-marshaled string -> apply
		|content media type / array -> reject: content media type is not supported on a array
		|content media type / base64 byte string -> apply
		|content media type / boolean -> reject: content media type is not supported on a boolean
		|content media type / number -> reject: content media type is not supported on a number
		|content media type / object -> reject: content media type is not supported on a object
		|content media type / opaque value -> reject: content media type is not supported on a opaque value
		|content media type / raw byte slice -> reject: content media type is not supported on a raw byte slice
		|content media type / string -> apply
		|content media type / string-coerced boolean -> apply
		|content media type / string-coerced number -> apply
		|content media type / text-marshaled string -> apply
		|divisor / array -> reject: divisor is not supported on a array
		|divisor / base64 byte string -> reject: divisor is not supported on a base64 byte string
		|divisor / boolean -> reject: divisor is not supported on a boolean
		|divisor / number -> apply
		|divisor / object -> reject: divisor is not supported on a object
		|divisor / opaque value -> reject: divisor is not supported on a opaque value
		|divisor / raw byte slice -> reject: divisor is not supported on a raw byte slice
		|divisor / string -> reject: divisor is not supported on a string
		|divisor / string-coerced boolean -> reject: divisor is not supported on a string-coerced boolean
		|divisor / string-coerced number -> reject: divisor is not supported on a string-coerced number
		|divisor / text-marshaled string -> reject: divisor is not supported on a text-marshaled string
		|element uniqueness / array -> apply
		|element uniqueness / base64 byte string -> reject: element uniqueness is not supported on a base64 byte string
		|element uniqueness / boolean -> reject: element uniqueness is not supported on a boolean
		|element uniqueness / number -> reject: element uniqueness is not supported on a number
		|element uniqueness / object -> ignore: unique on a map asserts distinct values, which no object keyword expresses
		|element uniqueness / opaque value -> reject: element uniqueness is not supported on a opaque value
		|element uniqueness / raw byte slice -> reject: element uniqueness is not supported on a raw byte slice
		|element uniqueness / string -> reject: element uniqueness is not supported on a string
		|element uniqueness / string-coerced boolean -> reject: element uniqueness is not supported on a string-coerced boolean
		|element uniqueness / string-coerced number -> reject: element uniqueness is not supported on a string-coerced number
		|element uniqueness / text-marshaled string -> reject: element uniqueness is not supported on a text-marshaled string
		|enumerated values / array -> apply
		|enumerated values / base64 byte string -> reject: a base64 byte string has no item schema to constrain
		|enumerated values / boolean -> apply
		|enumerated values / number -> apply
		|enumerated values / object -> reject: an enumeration on a map has no element meaning (use dive to constrain the values)
		|enumerated values / opaque value -> reject: enumerated values is not supported on a opaque value
		|enumerated values / raw byte slice -> reject: a raw byte slice has no item schema to constrain
		|enumerated values / string -> apply
		|enumerated values / string-coerced boolean -> apply
		|enumerated values / string-coerced number -> apply
		|enumerated values / text-marshaled string -> reject: a text-marshaled value cannot be compared against a tag literal (the tag has no way to spell the marshaled form)
		|exact size / array -> apply
		|exact size / base64 byte string -> apply
		|exact size / boolean -> reject: exact size is not supported on a boolean
		|exact size / number -> apply
		|exact size / object -> apply
		|exact size / opaque value -> reject: exact size is not supported on a opaque value
		|exact size / raw byte slice -> reject: exact size is not supported on a raw byte slice
		|exact size / string -> apply
		|exact size / string-coerced boolean -> reject: exact size is not supported on a string-coerced boolean
		|exact size / string-coerced number -> apply
		|exact size / text-marshaled string -> apply
		|exclusive ceiling / array -> apply
		|exclusive ceiling / base64 byte string -> apply
		|exclusive ceiling / boolean -> reject: exclusive ceiling is not supported on a boolean
		|exclusive ceiling / number -> apply
		|exclusive ceiling / object -> apply
		|exclusive ceiling / opaque value -> reject: exclusive ceiling is not supported on a opaque value
		|exclusive ceiling / raw byte slice -> reject: exclusive ceiling is not supported on a raw byte slice
		|exclusive ceiling / string -> apply
		|exclusive ceiling / string-coerced boolean -> reject: a bound has no faithful mapping onto a json:",string" coerced field (minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)
		|exclusive ceiling / string-coerced number -> reject: a bound has no faithful mapping onto a json:",string" coerced field (minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)
		|exclusive ceiling / text-marshaled string -> apply
		|exclusive floor / array -> apply
		|exclusive floor / base64 byte string -> apply
		|exclusive floor / boolean -> reject: exclusive floor is not supported on a boolean
		|exclusive floor / number -> apply
		|exclusive floor / object -> apply
		|exclusive floor / opaque value -> reject: exclusive floor is not supported on a opaque value
		|exclusive floor / raw byte slice -> reject: exclusive floor is not supported on a raw byte slice
		|exclusive floor / string -> apply
		|exclusive floor / string-coerced boolean -> reject: a bound has no faithful mapping onto a json:",string" coerced field (minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)
		|exclusive floor / string-coerced number -> reject: a bound has no faithful mapping onto a json:",string" coerced field (minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)
		|exclusive floor / text-marshaled string -> apply
		|forbidden size / array -> apply
		|forbidden size / base64 byte string -> reject: forbidden size is not supported on a base64 byte string
		|forbidden size / boolean -> reject: forbidden size is not supported on a boolean
		|forbidden size / number -> reject: forbidden size is not supported on a number
		|forbidden size / object -> apply
		|forbidden size / opaque value -> reject: forbidden size is not supported on a opaque value
		|forbidden size / raw byte slice -> reject: forbidden size is not supported on a raw byte slice
		|forbidden size / string -> reject: forbidden size is not supported on a string
		|forbidden size / string-coerced boolean -> reject: forbidden size is not supported on a string-coerced boolean
		|forbidden size / string-coerced number -> reject: forbidden size is not supported on a string-coerced number
		|forbidden size / text-marshaled string -> reject: forbidden size is not supported on a text-marshaled string
		|forbidden value / array -> reject: forbidden value is not supported on a array
		|forbidden value / base64 byte string -> reject: forbidden value is not supported on a base64 byte string
		|forbidden value / boolean -> apply
		|forbidden value / number -> apply
		|forbidden value / object -> reject: forbidden value is not supported on a object
		|forbidden value / opaque value -> reject: forbidden value is not supported on a opaque value
		|forbidden value / raw byte slice -> reject: forbidden value is not supported on a raw byte slice
		|forbidden value / string -> apply
		|forbidden value / string-coerced boolean -> apply
		|forbidden value / string-coerced number -> apply
		|forbidden value / text-marshaled string -> reject: a text-marshaled value cannot be compared against a tag literal (the tag has no way to spell the marshaled form)
		|format / array -> reject: format is not supported on a array
		|format / base64 byte string -> apply
		|format / boolean -> reject: format is not supported on a boolean
		|format / number -> reject: format is not supported on a number
		|format / object -> reject: format is not supported on a object
		|format / opaque value -> reject: format is not supported on a opaque value
		|format / raw byte slice -> reject: format is not supported on a raw byte slice
		|format / string -> apply
		|format / string-coerced boolean -> apply
		|format / string-coerced number -> apply
		|format / text-marshaled string -> apply
		|inclusive ceiling / array -> apply
		|inclusive ceiling / base64 byte string -> apply
		|inclusive ceiling / boolean -> reject: inclusive ceiling is not supported on a boolean
		|inclusive ceiling / number -> apply
		|inclusive ceiling / object -> apply
		|inclusive ceiling / opaque value -> reject: inclusive ceiling is not supported on a opaque value
		|inclusive ceiling / raw byte slice -> reject: inclusive ceiling is not supported on a raw byte slice
		|inclusive ceiling / string -> apply
		|inclusive ceiling / string-coerced boolean -> reject: a bound has no faithful mapping onto a json:",string" coerced field (minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)
		|inclusive ceiling / string-coerced number -> reject: a bound has no faithful mapping onto a json:",string" coerced field (minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)
		|inclusive ceiling / text-marshaled string -> apply
		|inclusive floor / array -> apply
		|inclusive floor / base64 byte string -> apply
		|inclusive floor / boolean -> reject: inclusive floor is not supported on a boolean
		|inclusive floor / number -> apply
		|inclusive floor / object -> apply
		|inclusive floor / opaque value -> reject: inclusive floor is not supported on a opaque value
		|inclusive floor / raw byte slice -> reject: inclusive floor is not supported on a raw byte slice
		|inclusive floor / string -> apply
		|inclusive floor / string-coerced boolean -> reject: a bound has no faithful mapping onto a json:",string" coerced field (minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)
		|inclusive floor / string-coerced number -> reject: a bound has no faithful mapping onto a json:",string" coerced field (minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)
		|inclusive floor / text-marshaled string -> apply
		|non-zero assertion / array -> apply
		|non-zero assertion / base64 byte string -> apply
		|non-zero assertion / boolean -> apply
		|non-zero assertion / number -> apply
		|non-zero assertion / object -> apply
		|non-zero assertion / opaque value -> ignore: an opaque value has no schema-expressible zero
		|non-zero assertion / raw byte slice -> ignore: an unconstrained raw JSON value has no faithful non-zero form
		|non-zero assertion / string -> apply
		|non-zero assertion / string-coerced boolean -> apply
		|non-zero assertion / string-coerced number -> apply
		|non-zero assertion / text-marshaled string -> ignore: a text-marshaled value has no schema-expressible zero
		|pattern / array -> reject: pattern is not supported on a array
		|pattern / base64 byte string -> apply
		|pattern / boolean -> reject: pattern is not supported on a boolean
		|pattern / number -> reject: pattern is not supported on a number
		|pattern / object -> reject: pattern is not supported on a object
		|pattern / opaque value -> reject: pattern is not supported on a opaque value
		|pattern / raw byte slice -> reject: pattern is not supported on a raw byte slice
		|pattern / string -> apply
		|pattern / string-coerced boolean -> apply
		|pattern / string-coerced number -> apply
		|pattern / text-marshaled string -> apply
		|pinned value / array -> reject: pinned value is not supported on a array
		|pinned value / base64 byte string -> reject: pinned value is not supported on a base64 byte string
		|pinned value / boolean -> apply
		|pinned value / number -> apply
		|pinned value / object -> reject: pinned value is not supported on a object
		|pinned value / opaque value -> reject: pinned value is not supported on a opaque value
		|pinned value / raw byte slice -> reject: pinned value is not supported on a raw byte slice
		|pinned value / string -> apply
		|pinned value / string-coerced boolean -> apply
		|pinned value / string-coerced number -> apply
		|pinned value / text-marshaled string -> reject: a text-marshaled value cannot be compared against a tag literal (the tag has no way to spell the marshaled form)
`)

	assert.Equal(t, want, strings.TrimSuffix(tagmodel.DumpMatrix(), "\n"))
}

// TestAxisCompatibilityTotal confirms every form resolves a bound to an accepted
// axis or a written rejection, never to silence. A form whose natural axis it
// does not carry is deliberate and load-bearing, not an oversight: it is what
// makes a rule-shaped min on a []byte an error while the pinned minLength on the
// same field applies.
func TestAxisCompatibilityTotal(t *testing.T) {
	t.Parallel()

	for _, form := range tagmodel.Forms() {
		for _, axis := range tagmodel.Axes() {
			_, err := tagmodel.ResolveAxisForTest(form, axis)
			if err == nil {
				continue
			}

			require.ErrorIs(t, err, tagmodel.ErrUnsupported,
				"a bound (%s, %s) cannot carry reports the shared sentinel", form, axis)
			assert.NotEqual(t, tagmodel.ErrUnsupported.Error(), err.Error(),
				"the rejection for (%s, %s) carries a reason of its own", form, axis)
		}
	}
}

// TestFormClassificationTotal confirms every reflect.Kind classifies, so no Go
// type reaches the matrix without a column. A kind that fell through would land
// on FormUnset, which Apply reports rather than dispatching on.
func TestFormClassificationTotal(t *testing.T) {
	t.Parallel()

	kinds := map[reflect.Kind]reflect.Type{
		reflect.Bool:          reflect.TypeFor[bool](),
		reflect.Int:           reflect.TypeFor[int](),
		reflect.Int8:          reflect.TypeFor[int8](),
		reflect.Int16:         reflect.TypeFor[int16](),
		reflect.Int32:         reflect.TypeFor[int32](),
		reflect.Int64:         reflect.TypeFor[int64](),
		reflect.Uint:          reflect.TypeFor[uint](),
		reflect.Uint8:         reflect.TypeFor[uint8](),
		reflect.Uint16:        reflect.TypeFor[uint16](),
		reflect.Uint32:        reflect.TypeFor[uint32](),
		reflect.Uint64:        reflect.TypeFor[uint64](),
		reflect.Uintptr:       reflect.TypeFor[uintptr](),
		reflect.Float32:       reflect.TypeFor[float32](),
		reflect.Float64:       reflect.TypeFor[float64](),
		reflect.Complex64:     reflect.TypeFor[complex64](),
		reflect.Complex128:    reflect.TypeFor[complex128](),
		reflect.Array:         reflect.TypeFor[[2]string](),
		reflect.Chan:          reflect.TypeFor[chan int](),
		reflect.Func:          reflect.TypeFor[func()](),
		reflect.Interface:     reflect.TypeFor[any](),
		reflect.Map:           reflect.TypeFor[map[string]int](),
		reflect.Pointer:       reflect.TypeFor[*int](),
		reflect.Slice:         reflect.TypeFor[[]string](),
		reflect.String:        reflect.TypeFor[string](),
		reflect.Struct:        reflect.TypeFor[struct{}](),
		reflect.UnsafePointer: reflect.TypeFor[unsafe.Pointer](),
	}

	for kind := reflect.Bool; kind <= reflect.UnsafePointer; kind++ {
		typ, ok := kinds[kind]
		require.True(t, ok, "kind %s has a representative type in this test", kind)

		assert.NotEqual(t, tagmodel.FormUnset, tagmodel.ShapeOf(typ, nil).Form,
			"kind %s classifies to a form", kind)
	}
}
