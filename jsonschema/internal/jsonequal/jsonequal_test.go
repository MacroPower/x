package jsonequal_test

import (
	"encoding/json"
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonequal"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// The validator wires these two entry points into the const/enum and
// uniqueItems keywords. EqualWithRat compares a schema-authored value (parsed
// without UseNumber, so a schema number is a float64) against a decoded
// instance (numbers arrive as json.Number); a precomputed *big.Rat fast-paths
// the top-level numeric case and nil falls back to the general comparison.
// HasDuplicates hashes then compares with the same JSON semantics, so equal
// values must share a hash bucket regardless of representation or map order.

func TestEqualWithRat(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schemaVal any
		// The schemaRat field mirrors the validator's precomputed top-level
		// numeric rat (numrat.SchemaNumberRat). It is nil for non-numeric schema
		// values and for the cases that exercise the recompute path.
		schemaRat *big.Rat
		instance  any
		want      bool
	}{
		"null matches null": {
			schemaVal: nil,
			instance:  nil,
			want:      true,
		},
		"null rejects non-null": {
			schemaVal: nil,
			instance:  false,
			want:      false,
		},
		"string equal": {
			schemaVal: "abc",
			instance:  "abc",
			want:      true,
		},
		"string unequal": {
			schemaVal: "abc",
			instance:  "abd",
			want:      false,
		},
		"bool never equals number": {
			schemaVal: true,
			instance:  json.Number("1"),
			want:      false,
		},
		"schema float matches json.Number across representations": {
			// Schema 1.0 parses to float64(1); instance decodes to "1.0".
			schemaVal: float64(1),
			instance:  json.Number("1.0"),
			want:      true,
		},
		"schema float matches json.Number with precomputed rat": {
			schemaVal: float64(1),
			schemaRat: ratOf(t, float64(1)),
			instance:  json.Number("1.0"),
			want:      true,
		},
		"schema 0.1 matches decimal literal 0.1 not its binary value": {
			schemaVal: float64(0.1),
			instance:  json.Number("0.1"),
			want:      true,
		},
		"numeric schema rejects non-numeric instance": {
			schemaVal: float64(1),
			instance:  "1",
			want:      false,
		},
		"numeric schema rejects unequal number": {
			schemaVal: float64(1),
			schemaRat: ratOf(t, float64(1)),
			instance:  json.Number("2"),
			want:      false,
		},
		"nested arrays equal across number representations": {
			schemaVal: []any{float64(1), float64(2)},
			instance:  []any{json.Number("1.0"), json.Number("2")},
			want:      true,
		},
		"objects equal ignoring key order": {
			schemaVal: map[string]any{"a": float64(1), "b": "x"},
			instance:  map[string]any{"b": "x", "a": json.Number("1")},
			want:      true,
		},
		"objects unequal on value": {
			schemaVal: map[string]any{"a": float64(1)},
			instance:  map[string]any{"a": json.Number("2")},
			want:      false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, jsonequal.EqualWithRat(tc.schemaVal, tc.schemaRat, tc.instance))
		})
	}
}

func TestHasDuplicates(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		arr  []any
		want bool
	}{
		"all distinct": {
			arr:  []any{json.Number("1"), json.Number("2"), "a", true},
			want: false,
		},
		"integer and decimal-one are duplicates": {
			arr:  []any{json.Number("1"), json.Number("1.0")},
			want: true,
		},
		"json.Number and float64 of equal value collide": {
			arr:  []any{json.Number("1"), float64(1)},
			want: true,
		},
		"objects equal regardless of key order": {
			arr: []any{
				map[string]any{"a": json.Number("1"), "b": json.Number("2")},
				map[string]any{"b": json.Number("2"), "a": json.Number("1")},
			},
			want: true,
		},
		"permuted-value objects are not duplicates": {
			arr: []any{
				map[string]any{"a": json.Number("1"), "b": json.Number("2")},
				map[string]any{"a": json.Number("2"), "b": json.Number("1")},
			},
			want: false,
		},
		"distinct nested arrays": {
			arr: []any{
				[]any{json.Number("1"), json.Number("2")},
				[]any{json.Number("1"), json.Number("3")},
			},
			want: false,
		},
		"duplicate strings": {
			arr:  []any{"x", "y", "x"},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, jsonequal.HasDuplicates(tc.arr))
		})
	}
}

// TestNonFiniteFloatNeverEqual covers the guard that strips upstream's collapse
// of NaN and ±Inf toward a single rational: such a value must be unequal to
// everything, including itself, and each must occupy its own hash bucket.
func TestNonFiniteFloatNeverEqual(t *testing.T) {
	t.Parallel()

	nan := math.NaN()
	posInf := math.Inf(1)
	negInf := math.Inf(-1)

	tests := map[string]struct {
		a, b any
	}{
		"NaN not equal to itself":     {a: nan, b: nan},
		"+Inf not equal to itself":    {a: posInf, b: posInf},
		"-Inf not equal to itself":    {a: negInf, b: negInf},
		"+Inf not equal to -Inf":      {a: posInf, b: negInf},
		"NaN not equal to zero":       {a: nan, b: float64(0)},
		"NaN inside array poisons it": {a: []any{nan}, b: []any{nan}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// A nil schemaRat routes through the general comparison, which is the
			// path that hits the non-finite guard.
			assert.False(t, jsonequal.EqualWithRat(tc.a, nil, tc.b))
		})
	}

	// Each non-finite float lands in a distinct bucket, so a slice of them all
	// reports no duplicates even though two share the value NaN textually.
	assert.False(t, jsonequal.HasDuplicates([]any{nan, posInf, negInf}))
}

// TestOverCapNumbers covers the DoS guard: numbers whose exponent exceeds the
// clamp are compared by canonical decomposition (and exact unclamped exponent),
// never by an uncapped big.Rat expansion.
func TestOverCapNumbers(t *testing.T) {
	t.Parallel()

	// Sanity: confirm these literals really are outside the cheap-expansion
	// bounds, so the comparison takes the guarded path rather than the rat path.
	for _, lit := range []string{"1e1000000000", "1e2000000000"} {
		d, ok := numrat.ParseDecNumber(lit)
		assert.True(t, ok, "parses: %s", lit)
		assert.False(t, d.ExactlyComparable(), "over cap: %s", lit)
	}

	tests := map[string]struct {
		a, b any
		want bool
	}{
		"identical huge magnitudes are equal": {
			a:    json.Number("1e1000000000"),
			b:    json.Number("1e1000000000"),
			want: true,
		},
		"distinct huge exponents stay distinct": {
			a:    json.Number("1e1000000000"),
			b:    json.Number("1e2000000000"),
			want: false,
		},
		"huge number never equals an in-range integer": {
			a:    json.Number("1e1000000000"),
			b:    json.Number("1"),
			want: false,
		},
		"huge number never equals a float": {
			a:    json.Number("1e1000000000"),
			b:    float64(1),
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, jsonequal.EqualWithRat(tc.a, nil, tc.b))
		})
	}

	// The hash agrees with equality: identical huge literals collide (duplicate),
	// distinct ones do not.
	assert.True(t, jsonequal.HasDuplicates([]any{json.Number("1e1000000000"), json.Number("1e1000000000")}))
	assert.False(t, jsonequal.HasDuplicates([]any{json.Number("1e1000000000"), json.Number("1e2000000000")}))
}

// ratOf mirrors the validator's top-level numeric precompute
// (numrat.SchemaNumberRat) for a schema value, failing the test if the value is
// not a recognized schema number.
func ratOf(t *testing.T, v any) *big.Rat {
	t.Helper()

	r, ok := numrat.SchemaNumberRat(v)
	if !ok {
		t.Fatalf("schema value %v is not numeric", v)
	}

	return r
}
