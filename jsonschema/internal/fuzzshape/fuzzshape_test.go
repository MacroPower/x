package fuzzshape_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
)

// drawCount is how many blobs each guard draws. It is capped because these are
// ordinary tests on the fast gate, not a fuzzing run: the population only has
// to be large enough to cover every shape class the diversity guard names.
const drawCount = 512

// reasonUnmodeledMarshaler is why no pooled type may marshal itself unless the
// generator models it exactly through a built-in override. It is stated with
// the guard that enforces it; the differential rig's own exclusion reasons live
// with the rig.
const reasonUnmodeledMarshaler = "a type marshaling itself is reflected by its Go struct shape, since MarshalJSON's output shape is unknowable via reflection, so what it marshals to is legitimately outside its generated schema and the differential rig would read the mismatch as a generator bug"

// overriddenMarshalers are the marshaler-implementing types the generator
// models exactly through a built-in override, so pooling them is safe.
var overriddenMarshalers = map[reflect.Type]bool{
	reflect.TypeFor[time.Time]():      true,
	reflect.TypeFor[jsontext.Value](): true,
	reflect.TypeFor[jsonv1.Number]():  true,
	reflect.TypeFor[big.Int]():        true,
}

// TestPoolsExcludeUnmodeledMarshalers asserts no drawn shape reaches a type
// that serializes itself without an override backing it, neither at the root
// nor anywhere inside a field. The pools are chosen to make that true; this is
// what keeps a later pool entry from quietly making it false.
func TestPoolsExcludeUnmodeledMarshalers(t *testing.T) {
	t.Parallel()

	for _, blob := range fuzzshape.Blobs(drawCount) {
		rt := fuzzshape.Type(blob)

		// The root marshaling itself means an embed promoted a marshaler, which
		// no embed pool entry has a method to do.
		require.False(t, marshalsItself(rt), reasonUnmodeledMarshaler)

		for field := range rt.Fields() {
			requireModeled(t, field.Type, 0)
		}
	}
}

// requireModeled walks a drawn field type and fails on any named type in it
// that marshals itself without a built-in override backing it.
func requireModeled(t *testing.T, rt reflect.Type, depth int) {
	t.Helper()

	// The pools compose at most a few levels, so the cap only stops a future
	// self-referential pool entry from recursing forever.
	if depth > 4 {
		return
	}

	if rt.Name() != "" && !overriddenMarshalers[rt] {
		require.Falsef(t, marshalsItself(rt), "%s: %s", rt, reasonUnmodeledMarshaler)
	}

	switch rt.Kind() { //nolint:exhaustive // only the kinds composing another type need descending into.
	case reflect.Pointer, reflect.Slice, reflect.Array:
		requireModeled(t, rt.Elem(), depth+1)

	case reflect.Map:
		requireModeled(t, rt.Key(), depth+1)
		requireModeled(t, rt.Elem(), depth+1)

	case reflect.Struct:
		for field := range rt.Fields() {
			requireModeled(t, field.Type, depth+1)
		}

	default:
	}
}

// marshalsItself reports whether encoding/json serializes a type through its
// own method rather than by walking its shape. It asks through reflectkind, the
// same classification the generator resolves types with, so the guard cannot
// drift from what the generation half actually decides.
func marshalsItself(rt reflect.Type) bool {
	return reflectkind.ImplementsJSONMarshaler(rt) ||
		rt.Implements(reflectkind.TypeTextMarshaler) ||
		reflect.PointerTo(rt).Implements(reflectkind.TypeTextMarshaler)
}

// TestTypeIsTotal asserts every blob yields a usable type: reflect.StructOf
// panics on a shape it cannot build, so a pool entry or draw constraint that
// regresses surfaces here instead of as fuzzing noise in the differential
// rig. Marshaling is deliberately not total. Some draws are declarations
// [encoding/json/v2] refuses, and the differential rigs assert generation
// refuses exactly those, so this guard requires only that refusals do not
// swallow the population: a healthy share of draws must still marshal, or the
// accept property has little to run on.
func TestTypeIsTotal(t *testing.T) {
	t.Parallel()

	marshaled := 0
	blobs := fuzzshape.Blobs(drawCount)

	for i, blob := range blobs {
		rt := fuzzshape.Type(blob)
		require.Equal(t, reflect.Struct, rt.Kind(), "draw %d is not a struct", i)

		val := reflect.New(rt)
		fuzzfill.Fill(val, blob, fuzzshape.FillOptions()...)

		_, err := json.Marshal(val.Interface())
		if err == nil {
			marshaled++
		}
	}

	require.Greater(t, marshaled, len(blobs)/2,
		"refused declarations swallow the population")
}

// TestTypeIsDeterministic asserts a blob always yields the same type, the
// property Go's fuzzing engine needs to reproduce and minimize a failing input.
// A pool selection that consulted map iteration order would defeat it.
func TestTypeIsDeterministic(t *testing.T) {
	t.Parallel()

	for i, blob := range fuzzshape.Blobs(drawCount) {
		first := fuzzshape.Type(blob)
		second := fuzzshape.Type(blob)

		// Identical shapes are interned by reflect.StructOf, so two identical
		// draws are the same runtime type and equality is the strongest check.
		require.Equal(t, first, second, "draw %d is not stable", i)
	}
}

// TestTypeDrawsDiverseShapes asserts the population actually contains every
// shape class the rig depends on. Without it an off-by-one in a pool index
// could collapse the generator to trivial structs, and the differential rig
// would stay green forever while covering nothing.
func TestTypeDrawsDiverseShapes(t *testing.T) {
	t.Parallel()

	classes := map[string]func(reflect.StructField) bool{
		"an embedded field": func(f reflect.StructField) bool {
			return f.Anonymous
		},
		"a tag name cut short by a reserved character": func(f reflect.StructField) bool {
			return strings.Contains(f.Tag.Get("json"), `"`)
		},
		"a ,string coercion": func(f reflect.StructField) bool {
			return strings.Contains(f.Tag.Get("json"), ",string")
		},
		"a multi-level pointer chain": func(f reflect.StructField) bool {
			return f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Pointer
		},
		`a json:"-" exclusion`: func(f reflect.StructField) bool {
			return f.Tag.Get("json") == "-"
		},
		"an untagged unexported field": func(f reflect.StructField) bool {
			// The draw never tags an unexported field, since encoding/json/v2
			// refuses the tagged form outright; the untagged form is the
			// exclusion class the pool exists for.
			return !f.IsExported()
		},
	}

	seen := make(map[string]bool, len(classes))

	for _, blob := range fuzzshape.Blobs(drawCount) {
		rt := fuzzshape.Type(blob)
		for field := range rt.Fields() {
			for name, match := range classes {
				if match(field) {
					seen[name] = true
				}
			}
		}
	}

	for name := range classes {
		require.True(t, seen[name], "no draw produced %s", name)
	}
}
