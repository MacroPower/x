package jsonvalue_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// The validator wires Equal into the const and enum keywords and HasDuplicates
// into uniqueItems. A schema-authored value converts through FromDocument
// (where the schema parser's float64 numbers take their shortest decimal) and
// an instance through FromGo, so the tests here build each side the way the
// validator does. HasDuplicates hashes then compares with the same JSON
// semantics, so equal values must share a hash bucket regardless of spelling
// or map order.

// document converts a schema-authored value, failing the test where the
// value has no document form.
func document(t *testing.T, v any) jsonvalue.Value {
	t.Helper()

	out, ok := jsonvalue.FromDocument(v)
	require.True(t, ok, "%#v has no document form", v)

	return out
}

// instance converts a Go instance, failing the test where the value is not
// accepted.
func instance(t *testing.T, v any) jsonvalue.Value {
	t.Helper()

	out, ok := jsonvalue.FromGo(v)
	require.True(t, ok, "%#v is not accepted", v)

	return out
}

// instances converts each element of a uniqueItems array.
func instances(t *testing.T, arr []any) []jsonvalue.Value {
	t.Helper()

	out := make([]jsonvalue.Value, len(arr))
	for i, v := range arr {
		out[i] = instance(t, v)
	}

	return out
}

func TestEqual(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schemaVal any
		instance  any
		want      bool
	}{
		"null matches null":     {schemaVal: nil, instance: nil, want: true},
		"null rejects non-null": {schemaVal: nil, instance: false, want: false},
		"string equal":          {schemaVal: "abc", instance: "abc", want: true},
		"string unequal":        {schemaVal: "abc", instance: "abd", want: false},
		"bool never equals number": {
			schemaVal: true,
			instance:  jsonv1.Number("1"),
			want:      false,
		},
		"schema float matches literal across representations": {
			// Schema 1.0 parses to float64(1); instance decodes to "1.0".
			schemaVal: float64(1),
			instance:  jsonv1.Number("1.0"),
			want:      true,
		},
		"schema 0.1 matches decimal literal 0.1 not its binary value": {
			schemaVal: float64(0.1),
			instance:  jsonv1.Number("0.1"),
			want:      true,
		},
		"numeric schema rejects non-numeric instance": {
			schemaVal: float64(1),
			instance:  "1",
			want:      false,
		},
		"numeric schema rejects unequal number": {
			schemaVal: float64(1),
			instance:  jsonv1.Number("2"),
			want:      false,
		},
		"go int against decoded float": {
			schemaVal: []any{1},
			instance:  []any{1.0},
			want:      true,
		},
		"go uint against literal": {
			schemaVal: map[string]any{"x": uint(7)},
			instance:  map[string]any{"x": jsonv1.Number("7")},
			want:      true,
		},
		"nested arrays equal across number representations": {
			schemaVal: []any{float64(1), float64(2)},
			instance:  []any{jsonv1.Number("1.0"), jsonv1.Number("2")},
			want:      true,
		},
		"arrays of different length": {
			schemaVal: []any{float64(1)},
			instance:  []any{jsonv1.Number("1"), jsonv1.Number("2")},
			want:      false,
		},
		"objects equal ignoring key order": {
			schemaVal: map[string]any{"a": float64(1), "b": "x"},
			instance:  map[string]any{"b": "x", "a": jsonv1.Number("1")},
			want:      true,
		},
		"objects unequal on value": {
			schemaVal: map[string]any{"a": float64(1)},
			instance:  map[string]any{"a": jsonv1.Number("2")},
			want:      false,
		},
		"objects unequal on key": {
			schemaVal: map[string]any{"a": float64(1)},
			instance:  map[string]any{"b": jsonv1.Number("1")},
			want:      false,
		},
		"typed slice against decoded number": {
			schemaVal: []int{1},
			instance:  []any{jsonv1.Number("1")},
			want:      true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, document(t, tc.schemaVal).Equal(instance(t, tc.instance)))
		})
	}
}

// TestEqualUnparseableLiteral pins the two sides of a Number literal outside
// the JSON number grammar: the document view refuses it, since v1 cannot
// render it, so as a schema value it equals nothing; as a Go instance it
// keeps its text and equals only another instance spelling the same text.
func TestEqualUnparseableLiteral(t *testing.T) {
	t.Parallel()

	_, ok := jsonvalue.FromDocument(jsonv1.Number("abc"))
	assert.False(t, ok, "the document view refuses a literal v1 cannot render")

	literal := instance(t, jsonv1.Number("abc"))
	assert.True(t, literal.Equal(instance(t, jsonv1.Number("abc"))))
	assert.False(t, literal.Equal(instance(t, jsonv1.Number("1"))))
}

func TestHasDuplicates(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		arr  []any
		want bool
	}{
		"all distinct": {
			arr:  []any{jsonv1.Number("1"), jsonv1.Number("2"), "a", true},
			want: false,
		},
		"integer and decimal-one are duplicates": {
			arr:  []any{jsonv1.Number("1"), jsonv1.Number("1.0")},
			want: true,
		},
		"literal and float64 of equal value collide": {
			arr:  []any{jsonv1.Number("1"), float64(1)},
			want: true,
		},
		"literal and float64 of equal non-integer value collide": {
			// The float is interpreted through its shortest decimal (1.1 as
			// 11/10), the way const, enum, and the numeric bounds interpret
			// it; its exact binary value never equals the literal.
			arr:  []any{jsonv1.Number("1.1"), float64(1.1)},
			want: true,
		},
		"literal and float64 of equal fraction collide": {
			arr:  []any{float64(0.1), jsonv1.Number("0.1")},
			want: true,
		},
		"distinct non-integer float64 and literal": {
			arr:  []any{float64(1.1), jsonv1.Number("1.2")},
			want: false,
		},
		"large integral float64 collides with its shortest-decimal literal": {
			// Above 2^53 the shortest decimal of an integral float can denote
			// a different integer than its exact binary value: float64(1<<60)
			// reads back as 1152921504606847000, so that literal is the same
			// value and must share its hash bucket.
			arr:  []any{float64(1 << 60), jsonv1.Number("1152921504606847000")},
			want: true,
		},
		"large integral float64 distinct from its exact binary integer": {
			arr:  []any{float64(1 << 60), jsonv1.Number("1152921504606846976")},
			want: false,
		},
		"objects equal regardless of key order": {
			arr: []any{
				map[string]any{"a": jsonv1.Number("1"), "b": jsonv1.Number("2")},
				map[string]any{"b": jsonv1.Number("2"), "a": jsonv1.Number("1")},
			},
			want: true,
		},
		"permuted-value objects are not duplicates": {
			arr: []any{
				map[string]any{"a": jsonv1.Number("1"), "b": jsonv1.Number("2")},
				map[string]any{"a": jsonv1.Number("2"), "b": jsonv1.Number("1")},
			},
			want: false,
		},
		"distinct nested arrays": {
			arr: []any{
				[]any{jsonv1.Number("1"), jsonv1.Number("2")},
				[]any{jsonv1.Number("1"), jsonv1.Number("3")},
			},
			want: false,
		},
		"duplicate strings": {
			arr:  []any{"x", "y", "x"},
			want: true,
		},
		"shared acyclic substructure still compares as duplicate": {
			arr:  []any{[]any{[]any{"a"}, []any{"a"}}, []any{[]any{"a"}, []any{"a"}}},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, jsonvalue.HasDuplicates(instances(t, tc.arr)))
		})
	}
}

// TestNonFiniteFloatNeverEqual pins that a NaN or infinity is unequal to
// everything, including itself, and never counts as a duplicate.
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

			assert.False(t, instance(t, tc.a).Equal(instance(t, tc.b)))
		})
	}

	assert.False(t, jsonvalue.HasDuplicates(instances(t, []any{nan, nan, posInf, negInf})))
}

// TestOverCapNumbers covers the DoS guard: numbers whose exponent exceeds the
// clamp are compared by canonical decomposition (and exact unclamped
// exponent), never by an uncapped big.Rat expansion.
func TestOverCapNumbers(t *testing.T) {
	t.Parallel()

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
			a:    jsonv1.Number("1e1000000000"),
			b:    jsonv1.Number("1e1000000000"),
			want: true,
		},
		"distinct huge exponents stay distinct": {
			a:    jsonv1.Number("1e1000000000"),
			b:    jsonv1.Number("1e2000000000"),
			want: false,
		},
		"huge number never equals an in-range integer": {
			a:    jsonv1.Number("1e1000000000"),
			b:    jsonv1.Number("1"),
			want: false,
		},
		"huge number never equals a float": {
			a:    jsonv1.Number("1e1000000000"),
			b:    float64(1),
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, document(t, tc.a).Equal(instance(t, tc.b)))
		})
	}

	// The hash agrees with equality: identical huge literals collide,
	// distinct ones do not.
	huge := jsonv1.Number("1e1000000000")
	assert.True(t, jsonvalue.HasDuplicates(instances(t, []any{huge, jsonv1.Number("1e1000000000")})))
	assert.False(t, jsonvalue.HasDuplicates(instances(t, []any{huge, jsonv1.Number("1e2000000000")})))
}

// TestOverLongCanonicallySmallNumbers covers literals whose raw length
// exceeds the expansion cap while their canonical decomposition is tiny
// (trailing fractional zeros strip to "1"). They compare through the
// canonical form in linear time; the wall-clock bound is the regression
// check against a quadratic parse of the raw literal.
func TestOverLongCanonicallySmallNumbers(t *testing.T) {
	t.Parallel()

	long1 := jsonv1.Number("1." + strings.Repeat("0", 4_000_000))
	long2 := jsonv1.Number("2." + strings.Repeat("0", 4_000_000))

	start := time.Now()

	assert.True(t, instance(t, long1).Equal(instance(t, jsonv1.Number(string(long1)))))
	assert.False(t, instance(t, long1).Equal(instance(t, long2)))
	assert.True(t, instance(t, long1).Equal(instance(t, jsonv1.Number("1"))))
	assert.True(t, jsonvalue.HasDuplicates(instances(t, []any{long1, jsonv1.Number(string(long1))})))
	assert.False(t, jsonvalue.HasDuplicates(instances(t, []any{long1, long2})))

	assert.Less(t, time.Since(start), 5*time.Second,
		"over-long literals must stay on the linear guarded path")
}

// TestOverCapExponentRunsCompareLinearly covers the exact-exponent tie-break
// for literals that share a clamped decomposition: every 1e<huge> literal
// with the same significand collides into one bucket, and only the exact
// canonical exponents can tell the values apart. That comparison must stay
// linear in the literal; the wall-clock bound is the regression check.
func TestOverCapExponentRunsCompareLinearly(t *testing.T) {
	t.Parallel()

	runLen := 8_000_000
	if testing.Short() {
		runLen = 100_000
	}

	run := strings.Repeat("7", runLen)

	// Distinct values sharing one clamped decomposition: same significand,
	// same clamped exponent, exponent runs differing only in the last digit.
	a := jsonv1.Number("1e" + run + "1")
	b := jsonv1.Number("1e" + run + "2")

	// Equal values spelled differently: 10e(X-1) shifts one significand
	// digit into the exponent, so the tie-break must prove the runs differ
	// by exactly the offset the significands contribute.
	c := jsonv1.Number("10e" + run + "0")
	d := jsonv1.Number("1e" + run + "1")

	start := time.Now()

	assert.False(t, jsonvalue.HasDuplicates(instances(t, []any{a, b})))
	assert.True(t, jsonvalue.HasDuplicates(instances(t, []any{c, d})))

	assert.Less(t, time.Since(start), 15*time.Second,
		"over-cap exponent runs must compare in linear time")
}

// TestEqualHandBuiltShapes pins the document view of the container shapes a
// hand-built const or enum can carry, most commonly the map[any]any
// gopkg.in/yaml.v2 decodes documents into. A string-keyed one is the object
// v1 writes for it; a bool-keyed one has no document form, and an Invalid
// value equals nothing, itself included.
func TestEqualHandBuiltShapes(t *testing.T) {
	t.Parallel()

	stringKeyed := document(t, map[any]any{"a": 1})
	assert.True(t, stringKeyed.Equal(instance(t, map[string]any{"a": 1.0})))
	assert.False(t, stringKeyed.Equal(instance(t, map[string]any{"a": 2.0})))

	nested := document(t, []any{map[any]any{"a": 1}})
	assert.True(t, nested.Equal(instance(t, []any{map[string]any{"a": 1.0}})))

	_, ok := jsonvalue.FromDocument(map[any]any{true: 1})
	assert.False(t, ok, "a bool-keyed map has no document form")

	fn := func() {}
	_, ok = jsonvalue.FromDocument(fn)
	assert.False(t, ok, "a func has no document form")

	invalid := jsonvalue.Value{}
	same := invalid
	assert.False(t, invalid.Equal(same))
	assert.False(t, invalid.Equal(jsonvalue.NewNull()))
	assert.Equal(t, invalid.Hash(), same.Hash())
}
