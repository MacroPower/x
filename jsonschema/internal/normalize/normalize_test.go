package normalize_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
)

func TestValueScalars(t *testing.T) {
	t.Parallel()

	const aboveFloat53 = int64(1)<<53 + 1

	tests := map[string]struct {
		in   any
		want any
	}{
		"int64 above 2^53 keeps exact value": {
			in:   aboveFloat53,
			want: json.Number("9007199254740993"),
		},
		"int widens to json.Number": {
			in:   42,
			want: json.Number("42"),
		},
		"uint64 max keeps exact value": {
			in:   uint64(math.MaxUint64),
			want: json.Number("18446744073709551615"),
		},
		"negative int8": {
			in:   int8(-7),
			want: json.Number("-7"),
		},
		"float32 widens to float64": {
			in:   float32(1.5),
			want: float64(1.5),
		},
		"float64 passes through": {
			in:   3.5,
			want: 3.5,
		},
		"json.Number passes through": {
			in:   json.Number("12.5"),
			want: json.Number("12.5"),
		},
		"string passes through": {
			in:   "hello",
			want: "hello",
		},
		"bool passes through": {
			in:   true,
			want: true,
		},
		"nil passes through": {
			in:   nil,
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, normalize.Value(tc.in))
		})
	}
}

func TestValueInt64AboveFloat53IsExactNotRounded(t *testing.T) {
	t.Parallel()

	// 2^53+1 is not representable as a float64; routing through float would
	// round it to 2^53. The json.Number must carry the exact decimal.
	const exact = int64(1)<<53 + 1

	got := normalize.Value(exact)

	num, ok := got.(json.Number)
	require.True(t, ok, "want json.Number, got %T", got)
	assert.Equal(t, json.Number("9007199254740993"), num)

	// A float round-trip loses the value; the int64 round-trip preserves it.
	gotInt, err := num.Int64()
	require.NoError(t, err)
	assert.Equal(t, exact, gotInt)
}

func TestValueFloat32Widening(t *testing.T) {
	t.Parallel()

	// 0.1 has no exact float32 or float64 representation; widening must produce
	// the float64 nearest to the float32 value, not the float64 nearest to 0.1.
	in := float32(0.1)

	got := normalize.Value(in)

	gotFloat, ok := got.(float64)
	require.True(t, ok, "want float64, got %T", got)
	// Compare bit patterns: the result must be the exact float64 widening of the
	// float32, not an approximation, so an exact bit equality is the assertion.
	assert.Equal(t, math.Float64bits(float64(in)), math.Float64bits(gotFloat))
}

func TestValueAlreadyJSONShapedNotMutated(t *testing.T) {
	t.Parallel()

	in := map[string]any{
		"s": "str",
		"n": json.Number("1"),
		"f": 2.0,
		"b": true,
		"arr": []any{
			"x",
			json.Number("3"),
		},
		"nested": map[string]any{
			"k": "v",
		},
	}

	got := normalize.Value(in)

	// Nothing changed, so the same map is returned and the input is intact.
	gotMap, ok := got.(map[string]any)
	require.True(t, ok, "want map[string]any, got %T", got)
	assert.Equal(t, in, gotMap)
	assert.Len(t, in, 6)
	assert.Equal(t, json.Number("1"), in["n"])

	inArr, ok := in["arr"].([]any)
	require.True(t, ok)
	assert.Equal(t, json.Number("3"), inArr[1])
}

func TestValueCopyOnChangeClonesOnlyChangedContainers(t *testing.T) {
	t.Parallel()

	// Outer map holds an unchanged nested map and a sibling that needs
	// normalization. Only the outer map is cloned; the unchanged nested map is
	// shared by identity, and the input is never mutated.
	unchanged := map[string]any{"k": "v"}
	in := map[string]any{
		"raw":    42, // forces an outer-level change
		"shared": unchanged,
	}

	got := normalize.Value(in)

	gotMap, ok := got.(map[string]any)
	require.True(t, ok, "want map[string]any, got %T", got)

	// The outer container is a fresh copy (input untouched).
	assert.Equal(t, 42, in["raw"], "input must not be mutated")
	assert.Equal(t, json.Number("42"), gotMap["raw"])

	// The unchanged nested map is shared by identity, not cloned.
	gotShared, ok := gotMap["shared"].(map[string]any)
	require.True(t, ok)
	assert.True(t,
		reflectSameMap(unchanged, gotShared),
		"unchanged nested map should be shared, not cloned",
	)
}

func TestValueSliceCopyOnChange(t *testing.T) {
	t.Parallel()

	unchanged := []any{"a", "b"}
	in := []any{unchanged, 7}

	got := normalize.Value(in)

	gotSlice, ok := got.([]any)
	require.True(t, ok, "want []any, got %T", got)
	assert.Equal(t, 7, in[1], "input must not be mutated")
	assert.Equal(t, json.Number("7"), gotSlice[1])

	gotShared, ok := gotSlice[0].([]any)
	require.True(t, ok)
	assert.True(t,
		reflectSameSlice(unchanged, gotShared),
		"unchanged nested slice should be shared, not cloned",
	)
}

func TestValueCycleGuardSelfReferentialMap(t *testing.T) {
	t.Parallel()

	m := map[string]any{"n": 1}
	m["self"] = m

	// A naive recursion would overflow the stack; the guard must terminate.
	got := normalize.Value(m)

	gotMap, ok := got.(map[string]any)
	require.True(t, ok, "want map[string]any, got %T", got)

	// The reachable scalar is still normalized; the back-edge is left as-is.
	assert.Equal(t, json.Number("1"), gotMap["n"])
	assert.Contains(t, gotMap, "self")
}

func TestValueCycleGuardSelfReferentialSlice(t *testing.T) {
	t.Parallel()

	s := make([]any, 2)
	s[0] = 1
	s[1] = s

	got := normalize.Value(s)

	gotSlice, ok := got.([]any)
	require.True(t, ok, "want []any, got %T", got)
	require.Len(t, gotSlice, 2)
	assert.Equal(t, json.Number("1"), gotSlice[0])
}

func TestValueChecked(t *testing.T) {
	t.Parallel()

	// ValueChecked normalizes like Value and additionally reports whether every
	// leaf is, after normalization, a JSON-shaped value the validation walk
	// accepts. A raw Go int is accepted because normalization converts it to a
	// json.Number; a struct leaf has no JSON shape and is not accepted.
	tests := map[string]struct {
		in       any
		accepted bool
	}{
		"nil":                    {in: nil, accepted: true},
		"bool":                   {in: true, accepted: true},
		"string":                 {in: "x", accepted: true},
		"float64":                {in: 1.5, accepted: true},
		"json.Number":            {in: json.Number("5"), accepted: true},
		"raw int converts":       {in: 5, accepted: true},
		"accepted slice":         {in: []any{1, "x", nil}, accepted: true},
		"accepted map":           {in: map[string]any{"a": 1}, accepted: true},
		"slice with struct leaf": {in: []any{struct{}{}}, accepted: false},
		"map with struct leaf":   {in: map[string]any{"a": struct{}{}}, accepted: false},
		"bare struct":            {in: struct{}{}, accepted: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, accepted := normalize.ValueChecked(tc.in)
			assert.Equal(t, tc.accepted, accepted)
		})
	}
}

func TestValueCheckedTerminatesOnCyclicInstance(t *testing.T) {
	t.Parallel()

	// A self-referential map/slice is the input shape Value tolerates; the
	// folded acceptance walk must terminate at the back-edge rather than overflow
	// the stack, and a cycle wrapping a rejected leaf still reports not-accepted.
	m := map[string]any{"n": 1}
	m["self"] = m
	_, accepted := normalize.ValueChecked(m)
	assert.True(t, accepted)

	bad := map[string]any{"leaf": struct{}{}}
	bad["self"] = bad
	_, accepted = normalize.ValueChecked(bad)
	assert.False(t, accepted)
}

func TestValueResliceIsNormalizedNotMistakenForCycle(t *testing.T) {
	t.Parallel()

	// A reslice shares the backing array's data pointer with its parent but is a
	// distinct, acyclic value. Keying the cycle guard on {pointer, len} keeps it
	// from being mistaken for a back-edge, so its element is still normalized.
	backing := []any{10, 20, 30}
	in := []any{backing, backing[:1]}

	got := normalize.Value(in)

	gotSlice, ok := got.([]any)
	require.True(t, ok, "want []any, got %T", got)
	require.Len(t, gotSlice, 2)

	full, ok := gotSlice[0].([]any)
	require.True(t, ok)
	require.Len(t, full, 3)
	assert.Equal(t, json.Number("10"), full[0])
	assert.Equal(t, json.Number("20"), full[1])
	assert.Equal(t, json.Number("30"), full[2])

	reslice, ok := gotSlice[1].([]any)
	require.True(t, ok)
	require.Len(t, reslice, 1)
	assert.Equal(t, json.Number("10"), reslice[0],
		"the reslice must be normalized, not skipped as a cycle")
}

// reflectSameMap reports whether two maps are the same underlying map value.
func reflectSameMap(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}

	a["__same_probe__"] = struct{}{}
	defer delete(a, "__same_probe__")

	_, ok := b["__same_probe__"]

	return ok
}

// reflectSameSlice reports whether two slices share the same backing array at
// the same length.
func reflectSameSlice(a, b []any) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}

	orig := a[0]
	probe := struct{ x int }{x: 1}
	a[0] = probe

	defer func() { a[0] = orig }()

	return b[0] == any(probe)
}
