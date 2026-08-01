package jsonequal_test

import (
	"encoding/json"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

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
		"json.Number and float64 of equal non-integer value collide": {
			// The float must be interpreted through its shortest decimal
			// (1.1 as 11/10), the way const, enum, and the numeric bounds
			// interpret it; its exact binary value never equals the literal.
			arr:  []any{json.Number("1.1"), float64(1.1)},
			want: true,
		},
		"json.Number and float64 of equal fraction collide": {
			arr:  []any{float64(0.1), json.Number("0.1")},
			want: true,
		},
		"distinct non-integer float64 and json.Number": {
			arr:  []any{float64(1.1), json.Number("1.2")},
			want: false,
		},
		"large integral float64 collides with its shortest-decimal literal": {
			// Above 2^53 the shortest decimal of an integral float can denote
			// a different integer than its exact binary value: float64(1<<60)
			// reads back as 1152921504606847000, so that literal is the same
			// value and must share its hash bucket.
			arr:  []any{float64(1 << 60), json.Number("1152921504606847000")},
			want: true,
		},
		"large integral float64 distinct from its exact binary integer": {
			// The exact binary value 1152921504606846976 is a different value
			// under the shortest-decimal interpretation, matching const/enum.
			arr:  []any{float64(1 << 60), json.Number("1152921504606846976")},
			want: false,
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

// TestOverLongCanonicallySmallNumbers covers literals whose raw length exceeds
// the expansion cap while their canonical decomposition is tiny (trailing
// fractional zeros strip to "1"). Classified by decomposition alone they look
// exactly comparable, and the fallback path would then hand the raw
// multi-megabyte literal to big.Rat.SetString, whose cost is quadratic in raw
// length. The guard keys on raw length, so these compare via the canonical
// form: correct results in linear time. The wall-clock bound is the regression
// check; the pre-fix quadratic path took tens of seconds at this size.
func TestOverLongCanonicallySmallNumbers(t *testing.T) {
	t.Parallel()

	long1 := json.Number("1." + strings.Repeat("0", 4_000_000))
	long2 := json.Number("2." + strings.Repeat("0", 4_000_000))

	start := time.Now()

	assert.True(t, jsonequal.EqualWithRat(long1, nil, json.Number(string(long1))))
	assert.False(t, jsonequal.EqualWithRat(long1, nil, long2))
	assert.True(t, jsonequal.EqualWithRat(long1, nil, json.Number("1")))
	assert.True(t, jsonequal.HasDuplicates([]any{long1, json.Number(string(long1))}))
	assert.False(t, jsonequal.HasDuplicates([]any{long1, long2}))

	assert.Less(t, time.Since(start), 5*time.Second,
		"over-long literals must stay on the linear guarded path")
}

// TestOverCapExponentRunsCompareLinearly covers the exact-exponent tie-break
// for literals that share a clamped DecNumber: every 1e<huge> literal with the
// same significand collides into one bucket, and only the exact canonical
// exponents can tell the values apart. That comparison must stay linear in
// the literal (numrat.DecCanonicalExpEqual); materializing each exponent run
// with big.Int.SetString is quadratic in the run length, which at this size
// took minutes of CPU for a payload of a few megabytes. The wall-clock bound
// is the regression check.
func TestOverCapExponentRunsCompareLinearly(t *testing.T) {
	t.Parallel()

	runLen := 8_000_000
	if testing.Short() {
		runLen = 100_000
	}

	run := strings.Repeat("7", runLen)

	// Distinct values sharing one clamped DecNumber: same significand, same
	// clamped exponent, exponent runs differing only in the last digit.
	a := json.Number("1e" + run + "1")
	b := json.Number("1e" + run + "2")

	// Equal values spelled differently: 10e(X-1) shifts one significand
	// digit into the exponent, so the tie-break must prove the runs differ
	// by exactly the offset the significands contribute.
	c := json.Number("10e" + run + "0")
	d := json.Number("1e" + run + "1")

	start := time.Now()

	assert.False(t, jsonequal.HasDuplicates([]any{a, b}))
	assert.True(t, jsonequal.HasDuplicates([]any{c, d}))

	assert.Less(t, time.Since(start), 15*time.Second,
		"over-cap exponent runs must compare in linear time")
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
