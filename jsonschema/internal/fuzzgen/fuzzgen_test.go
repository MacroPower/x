package fuzzgen_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzgen"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
)

// drawCount is how many blobs each guard draws, fuzzshape's own count.
const drawCount = 512

// TestTaggedTypeIsTotalAndDeterministic is fuzzshape's TestTypeIsTotal and
// TestTypeIsDeterministic over the tagged draw: every blob yields a type, and
// the same type each time.
func TestTaggedTypeIsTotalAndDeterministic(t *testing.T) {
	t.Parallel()

	for i, blob := range fuzzshape.Blobs(drawCount) {
		first := fuzzgen.TaggedType(blob)
		second := fuzzgen.TaggedType(blob)

		require.Equal(t, first, second, "tagged draw %d is not stable", i)
	}
}

// TestTaggedTypeDrawsEveryClass asserts the tagged population reaches every
// class the generation invariants rig exists for, so a pool or odds change
// cannot quietly stop drawing one.
func TestTaggedTypeDrawsEveryClass(t *testing.T) {
	t.Parallel()

	classes := map[string]func(reflect.StructField) bool{
		"a jsonschema tag": func(f reflect.StructField) bool {
			return f.Tag.Get("jsonschema") != ""
		},
		"a validate tag": func(f reflect.StructField) bool {
			return f.Tag.Get("validate") != ""
		},
		"both dialects on one field": func(f reflect.StructField) bool {
			return f.Tag.Get("jsonschema") != "" && f.Tag.Get("validate") != ""
		},
		"a jsonschema tag on a ,string field": func(f reflect.StructField) bool {
			return f.Tag.Get("jsonschema") != "" && strings.Contains(f.Tag.Get("json"), ",string")
		},
		"a validate tag with a dive": func(f reflect.StructField) bool {
			return strings.Contains(f.Tag.Get("validate"), "dive")
		},
		"a provider type": func(f reflect.StructField) bool {
			return namedIn(f.Type) == reflect.TypeFor[fuzzgen.Provided]()
		},
		"an editing extender": func(f reflect.StructField) bool {
			return namedIn(f.Type) == reflect.TypeFor[fuzzgen.Edited]()
		},
		"a replacing extender": func(f reflect.StructField) bool {
			return namedIn(f.Type) == reflect.TypeFor[fuzzgen.Replaced]()
		},
		"a text marshaler": func(f reflect.StructField) bool {
			return namedIn(f.Type) == reflect.TypeFor[fuzzgen.Stamp]()
		},
		"a promoted JSON marshaler": func(f reflect.StructField) bool {
			return namedIn(f.Type) == reflect.TypeFor[fuzzgen.Marked]()
		},
		"a self-recursive type": func(f reflect.StructField) bool {
			return namedIn(f.Type) == reflect.TypeFor[fuzzgen.Ring]()
		},
		"a mutually recursive type": func(f reflect.StructField) bool {
			n := namedIn(f.Type)

			return n == reflect.TypeFor[fuzzgen.Ping]() || n == reflect.TypeFor[fuzzgen.Pong]()
		},
		"a by-value re-entrant type": func(f reflect.StructField) bool {
			return namedIn(f.Type) == reflect.TypeFor[fuzzgen.Chain]()
		},
	}

	seen := make(map[string]bool, len(classes))

	for _, blob := range fuzzshape.Blobs(drawCount) {
		for field := range fuzzgen.TaggedType(blob).Fields() {
			for name, match := range classes {
				if match(field) {
					seen[name] = true
				}
			}
		}
	}

	for name := range classes {
		require.True(t, seen[name], "no tagged draw produced %s", name)
	}
}

// TestConstrainedAndRecursiveReadTheDraw pins the two predicates the rig
// branches on against the population: a shape is constrained exactly when a
// field carries a dialect tag or reaches a hook type, and recursive exactly
// when a field reaches a recursive pool type, and both answers occur.
func TestConstrainedAndRecursiveReadTheDraw(t *testing.T) {
	t.Parallel()

	var constrained, plain, recursive, acyclic int

	for _, blob := range fuzzshape.Blobs(drawCount) {
		rt := fuzzgen.TaggedType(blob)

		if fuzzgen.Constrained(rt) {
			constrained++
		} else {
			plain++

			for field := range rt.Fields() {
				require.Empty(t, field.Tag.Get("jsonschema"), "%s is not constrained", rt)
				require.Empty(t, field.Tag.Get("validate"), "%s is not constrained", rt)
			}
		}

		if fuzzgen.Recursive(rt) {
			recursive++
		} else {
			acyclic++
		}
	}

	require.Positive(t, constrained)
	require.Positive(t, plain, "the accept property has nothing to run on")
	require.Positive(t, recursive)
	require.Positive(t, acyclic, "the Inline comparison has nothing to run on")
}

// TestTagParamsCoverEveryKey holds the jsonschema tag draw's parameter table
// to the parser's vocabulary in both directions, so a key added to the parser
// is drawn and a key dropped from it is not.
func TestTagParamsCoverEveryKey(t *testing.T) {
	t.Parallel()

	want := tagparse.Keys()
	got := fuzzgen.TagParamKeys()
	slices.Sort(got)

	require.Equal(t, want, got)
}

// TestOptionsIsDeterministicAndDrawsEveryClass asserts the options draw is
// stable per blob and that the population reaches both drafts, both
// definitions settings, an override, a defaults request, and a constraining
// override.
func TestOptionsIsDeterministicAndDrawsEveryClass(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}

	for i, blob := range fuzzshape.Blobs(drawCount) {
		first := fuzzgen.Options(blob)
		second := fuzzgen.Options(blob)

		require.Equal(t, first.Draft, second.Draft, "options draw %d is not stable", i)
		require.Equal(t, first.Definitions, second.Definitions, "options draw %d is not stable", i)
		require.Len(t, second.Opts, len(first.Opts), "options draw %d is not stable", i)
		require.Len(t, second.Handed, len(first.Handed), "options draw %d is not stable", i)

		seen["draft 7"] = seen["draft 7"] || first.Draft == jsonschema.Draft7
		seen["draft 2020-12"] = seen["draft 2020-12"] || first.Draft == jsonschema.Draft2020
		seen["definitions off"] = seen["definitions off"] || !first.Definitions
		seen["definitions on"] = seen["definitions on"] || first.Definitions
		seen["an override"] = seen["an override"] || len(first.Handed) > 0
		seen["defaults"] = seen["defaults"] || first.Defaults
		seen["a constraining override"] = seen["a constraining override"] || first.Constrains
	}

	for _, class := range []string{
		"draft 7", "draft 2020-12", "definitions off", "definitions on",
		"an override", "defaults", "a constraining override",
	} {
		require.True(t, seen[class], "no options draw produced %s", class)
	}
}

// namedIn mirrors the package's field type lookup for the class checks.
func namedIn(t reflect.Type) reflect.Type {
	for t.Name() == "" {
		switch t.Kind() { //nolint:exhaustive // only the composing kinds descend.
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		default:
			return t
		}
	}

	return t
}
