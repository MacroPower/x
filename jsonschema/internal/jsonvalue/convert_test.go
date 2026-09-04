package jsonvalue_test

import (
	"encoding/json/jsontext"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
)

func TestFromGoScalars(t *testing.T) {
	t.Parallel()

	const aboveFloat53 = int64(1)<<53 + 1

	tests := map[string]struct {
		in   any
		want any
	}{
		"int64 above 2^53 keeps exact value": {in: aboveFloat53, want: jsonv1.Number("9007199254740993")},
		"int widens to a literal":            {in: 42, want: jsonv1.Number("42")},
		"uint64 max keeps exact value":       {in: uint64(math.MaxUint64), want: jsonv1.Number("18446744073709551615")},
		"negative int8":                      {in: int8(-7), want: jsonv1.Number("-7")},
		"uintptr":                            {in: uintptr(7), want: jsonv1.Number("7")},
		"float32 widens to float64":          {in: float32(1.5), want: float64(1.5)},
		"float64 passes through":             {in: 3.5, want: 3.5},
		"literal passes through":             {in: jsonv1.Number("12.5"), want: jsonv1.Number("12.5")},
		"string passes through":              {in: "hello", want: "hello"},
		"bool passes through":                {in: true, want: true},
		"nil passes through":                 {in: nil, want: nil},
		"nested map and slice": {
			in: map[string]any{
				"age":  30,
				"tags": []any{uint8(1), "a", float32(2)},
				"sub":  map[string]any{"n": int64(5)},
			},
			want: map[string]any{
				"age":  jsonv1.Number("30"),
				"tags": []any{jsonv1.Number("1"), "a", float64(2)},
				"sub":  map[string]any{"n": jsonv1.Number("5")},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := jsonvalue.FromGo(tc.in)
			require.True(t, ok)
			assert.Equal(t, tc.want, got.Interface())
		})
	}
}

// TestFromGoFloat32Widening pins that a float32 widens to the float64 nearest
// the float32 value, not the float64 nearest the decimal it was written as,
// so float32(0.1) validates as 0.10000000149011612.
func TestFromGoFloat32Widening(t *testing.T) {
	t.Parallel()

	in := float32(0.1)

	got, ok := jsonvalue.FromGo(in)
	require.True(t, ok)
	assert.Equal(t, "0.10000000149011612", got.Literal())

	f, isFloat := got.Interface().(float64)
	require.True(t, isFloat)
	assert.Equal(t, math.Float64bits(float64(in)), math.Float64bits(f))
}

func TestFromGoRefusals(t *testing.T) {
	t.Parallel()

	cyclicMap := map[string]any{"n": 1}
	cyclicMap["self"] = cyclicMap

	cyclicSlice := make([]any, 2)
	cyclicSlice[0] = 1
	cyclicSlice[1] = cyclicSlice

	// The back-edge sits inside an intermediate container that itself needs
	// no scalar change, so the cycle is only reachable transitively.
	transitive := map[string]any{"n": 1}
	transitive["a"] = map[string]any{"x": transitive}

	tests := map[string]struct {
		in       any
		accepted bool
	}{
		"nil":                    {in: nil, accepted: true},
		"bool":                   {in: true, accepted: true},
		"string":                 {in: "x", accepted: true},
		"float64":                {in: 1.5, accepted: true},
		"literal":                {in: jsonv1.Number("5"), accepted: true},
		"raw int converts":       {in: 5, accepted: true},
		"accepted slice":         {in: []any{1, "x", nil}, accepted: true},
		"accepted map":           {in: map[string]any{"a": 1}, accepted: true},
		"slice with struct leaf": {in: []any{struct{}{}}, accepted: false},
		"map with struct leaf":   {in: map[string]any{"a": struct{}{}}, accepted: false},
		"bare struct":            {in: struct{}{}, accepted: false},
		"map[any]any":            {in: map[any]any{"a": 1}, accepted: false},
		"self-referential map":   {in: cyclicMap, accepted: false},
		"self-referential slice": {in: cyclicSlice, accepted: false},
		"transitive map cycle":   {in: transitive, accepted: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, accepted := jsonvalue.FromGo(tc.in)
			assert.Equal(t, tc.accepted, accepted)
		})
	}
}

// TestFromGoResliceIsNotACycle pins the {pointer, len} cycle key: a reslice
// shares the backing array's data pointer with its parent but is a distinct,
// acyclic value, so it converts rather than refusing as a back-edge.
func TestFromGoResliceIsNotACycle(t *testing.T) {
	t.Parallel()

	backing := []any{10, 20, 30}
	in := []any{backing, backing[:1]}

	got, ok := jsonvalue.FromGo(in)
	require.True(t, ok)
	assert.Equal(t, []any{
		[]any{jsonv1.Number("10"), jsonv1.Number("20"), jsonv1.Number("30")},
		[]any{jsonv1.Number("10")},
	}, got.Interface())
}

// panicMarshaler is a value whose own marshaler panics, which FromDocument
// must report as no JSON form rather than let escape.
type panicMarshaler struct{}

func (panicMarshaler) MarshalJSON() ([]byte, error) {
	panic("marshal")
}

// TestFromDocument pins the encoding/json v1 rendering FromDocument takes: a
// typed nil container or pointer is null, an empty non-nil container keeps
// its shape, a struct becomes an object, a raw value decodes to what it
// holds, and a string v1 escapes comes back unescaped. The four accepted
// shapes that still take the render come back as v1 writes them: a float32
// as its 32-bit shortest decimal, an empty Number as 0, and invalid UTF-8 in
// a string or member name as U+FFFD, each at the top level and nested. The
// refusals cover a value with no JSON form and a marshaler that panics.
func TestFromDocument(t *testing.T) {
	t.Parallel()

	type pair struct {
		A int    `json:"a"`
		B string `json:"b"`
	}

	tests := map[string]struct {
		in   any
		want any
		ok   bool
	}{
		"untyped nil":       {in: nil, want: nil, ok: true},
		"typed nil slice":   {in: []string(nil), want: nil, ok: true},
		"typed nil any":     {in: []any(nil), want: nil, ok: true},
		"typed nil map":     {in: map[string]int(nil), want: nil, ok: true},
		"typed nil any map": {in: map[string]any(nil), want: nil, ok: true},
		"nested nil slice": {
			in:   map[string]any{"k": []any(nil), "n": 1},
			want: map[string]any{"k": nil, "n": jsonv1.Number("1")},
			ok:   true,
		},
		"nested nil map": {
			in:   []any{map[string]any(nil), "x"},
			want: []any{nil, "x"},
			ok:   true,
		},
		"typed nil pointer": {in: (*int)(nil), want: nil, ok: true},
		"empty slice":       {in: []any{}, want: []any{}, ok: true},
		"empty typed slice": {in: []string{}, want: []any{}, ok: true},
		"empty map":         {in: map[string]any{}, want: map[string]any{}, ok: true},
		"typed slice":       {in: []string{"a"}, want: []any{"a"}, ok: true},
		"struct": {
			in:   pair{A: 1, B: "x"},
			want: map[string]any{"a": jsonv1.Number("1"), "b": "x"},
			ok:   true,
		},
		"pointer to struct": {
			in:   &pair{A: 2},
			want: map[string]any{"a": jsonv1.Number("2"), "b": ""},
			ok:   true,
		},
		"raw null":  {in: jsontext.Value("null"), want: nil, ok: true},
		"raw array": {in: jsontext.Value("[1,2]"), want: []any{jsonv1.Number("1"), jsonv1.Number("2")}, ok: true},
		"int":       {in: 7, want: jsonv1.Number("7"), ok: true},
		"string with angle bracket": {
			in:   "a<b>&c",
			want: "a<b>&c",
			ok:   true,
		},
		"escaped string through the marshal": {
			in:   []string{"a<b>&c"},
			want: []any{"a<b>&c"},
			ok:   true,
		},
		"already shaped container": {
			in:   map[string]any{"k": []any{"v", nil}},
			want: map[string]any{"k": []any{"v", nil}},
			ok:   true,
		},
		"float32":        {in: float32(0.1), want: jsonv1.Number("0.1"), ok: true},
		"nested float32": {in: []any{float32(0.1)}, want: []any{jsonv1.Number("0.1")}, ok: true},
		"empty number":   {in: jsonv1.Number(""), want: jsonv1.Number("0"), ok: true},
		"nested empty number": {
			in:   map[string]any{"n": jsonv1.Number("")},
			want: map[string]any{"n": jsonv1.Number("0")},
			ok:   true,
		},
		"unparseable number keeps its text": {in: jsonv1.Number("abc"), want: jsonv1.Number("abc"), ok: true},
		"invalid UTF-8 string":              {in: "\xff", want: "�", ok: true},
		"invalid UTF-8 run":                 {in: "a\xff\xffb", want: "a��b", ok: true},
		"nested invalid UTF-8 string":       {in: []any{"a", "\xff"}, want: []any{"a", "�"}, ok: true},
		"invalid UTF-8 member name": {
			in:   map[string]any{"\xff": 1},
			want: map[string]any{"�": jsonv1.Number("1")},
			ok:   true,
		},
		"nested invalid UTF-8 member name": {
			in:   []any{map[string]any{"k\xff": "v"}},
			want: []any{map[string]any{"k�": "v"}},
			ok:   true,
		},
		"string-keyed map[any]any": {
			in:   map[any]any{"a": 1},
			want: map[string]any{"a": jsonv1.Number("1")},
			ok:   true,
		},
		"bool-keyed map[any]any": {in: map[any]any{true: 1}, want: nil, ok: false},
		"chan":                   {in: make(chan int), want: nil, ok: false},
		"func":                   {in: func() {}, want: nil, ok: false},
		"panicking marshaler":    {in: panicMarshaler{}, want: nil, ok: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := jsonvalue.FromDocument(tt.in)
			require.Equal(t, tt.ok, ok)

			if ok {
				assert.Equal(t, tt.want, got.Interface())
			} else {
				assert.Equal(t, jsonvalue.Invalid, got.Kind())
			}
		})
	}
}

// TestFromDocumentNonFinite pins that a NaN or infinity keeps its Go-float
// identity through the document view: v1 refuses to marshal it, the fast
// path keeps it, and it equals nothing.
func TestFromDocumentNonFinite(t *testing.T) {
	t.Parallel()

	got, ok := jsonvalue.FromDocument(math.NaN())
	require.True(t, ok)
	assert.Equal(t, jsonvalue.Number, got.Kind())
	assert.False(t, got.Comparable())

	same := got
	assert.False(t, got.Equal(same))
}

func TestFromDocumentCycleRefused(t *testing.T) {
	t.Parallel()

	cyclic := map[string]any{}
	cyclic["self"] = cyclic

	_, ok := jsonvalue.FromDocument(cyclic)
	assert.False(t, ok)

	typed := map[any]any{}
	typed["self"] = typed

	_, ok = jsonvalue.FromDocument(typed)
	assert.False(t, ok)
}

type customString string

// TestExact pins Exact's outputs as literals: the exact integer text above
// 2^53, the v2 encoder's shortest-decimal float forms (float32's 32-bit one
// included), the negative-zero sign, container rebuilds, and the refusals.
// Literals rather than an oracle on purpose: Exact is the marshal plus
// exact-decode round trip, so an oracle spelled the same way would compare
// the implementation to itself and pin nothing.
func TestExact(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   any
		want any
		ok   bool
	}{
		"nil":    {in: nil, want: nil, ok: true},
		"bool":   {in: true, want: true, ok: true},
		"string": {in: "hello", want: "hello", ok: true},
		"number literal": {
			in:   jsonv1.Number("9007199254740993"),
			want: jsonv1.Number("9007199254740993"),
			ok:   true,
		},
		"number exponent literal": {in: jsonv1.Number("1e2"), want: jsonv1.Number("1e2"), ok: true},
		"empty number literal":    {in: jsonv1.Number(""), want: jsonv1.Number("0"), ok: true},
		"float64 fraction":        {in: 0.1, want: jsonv1.Number("0.1"), ok: true},
		"float64 negative zero":   {in: math.Copysign(0, -1), want: jsonv1.Number("-0"), ok: true},
		"float64 large":           {in: 1e21, want: jsonv1.Number("1e+21"), ok: true},
		"float64 small":           {in: 1e-9, want: jsonv1.Number("1e-9"), ok: true},
		"float64 above 2^53":      {in: float64(1 << 60), want: jsonv1.Number("1152921504606847000"), ok: true},
		"float32 fraction":        {in: float32(0.1), want: jsonv1.Number("0.1"), ok: true},
		"int":                     {in: -42, want: jsonv1.Number("-42"), ok: true},
		"int64 large":             {in: int64(1)<<62 + 1, want: jsonv1.Number("4611686018427387905"), ok: true},
		"uint64 above 2^53":       {in: uint64(1)<<60 + 1, want: jsonv1.Number("1152921504606846977"), ok: true},
		"nested containers": {
			in: map[string]any{
				"list": []any{jsonv1.Number("1"), 0.5, "x", nil},
				"map":  map[string]any{"k": uint64(1)<<60 + 1},
			},
			want: map[string]any{
				"list": []any{jsonv1.Number("1"), jsonv1.Number("0.5"), "x", nil},
				"map":  map[string]any{"k": jsonv1.Number("1152921504606846977")},
			},
			ok: true,
		},
		"empty map":       {in: map[string]any{}, want: map[string]any{}, ok: true},
		"nil map":         {in: map[string]any(nil), want: map[string]any{}, ok: true},
		"empty slice":     {in: []any{}, want: []any{}, ok: true},
		"nil slice":       {in: []any(nil), want: []any{}, ok: true},
		"struct fallback": {in: struct{ A int }{A: 1}, want: map[string]any{"A": jsonv1.Number("1")}, ok: true},
		"raw message fallback": {
			in:   jsonv1.RawMessage(`{"n":9007199254740993}`),
			want: map[string]any{"n": jsonv1.Number("9007199254740993")},
			ok:   true,
		},
		"raw value fallback": {
			in:   jsontext.Value(`[1,2]`),
			want: []any{jsonv1.Number("1"), jsonv1.Number("2")},
			ok:   true,
		},
		"named string fallback":  {in: customString("x"), want: "x", ok: true},
		"invalid number literal": {in: jsonv1.Number("abc")},
		"nan":                    {in: math.NaN()},
		"positive infinity":      {in: math.Inf(1)},
		"invalid utf8 string":    {in: "a\xffb"},
		"invalid utf8 map key":   {in: map[string]any{"a\xffb": 1}},
		"unmarshalable fallback": {in: make(chan int)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := jsonvalue.Exact(tt.in)

			require.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExactUnaliased(t *testing.T) {
	t.Parallel()

	src := map[string]any{
		"list": []any{jsonv1.Number("1"), map[string]any{"k": "v"}},
	}

	got, ok := jsonvalue.Exact(src)
	require.True(t, ok)

	cp, isMap := got.(map[string]any)
	require.True(t, isMap)

	// Mutate every container in the copy; the source must not observe it.
	cp["extra"] = true
	list, isList := cp["list"].([]any)
	require.True(t, isList)

	list[0] = jsonv1.Number("2")
	inner, isInner := list[1].(map[string]any)
	require.True(t, isInner)

	inner["k"] = "changed"

	assert.Equal(t, map[string]any{
		"list": []any{jsonv1.Number("1"), map[string]any{"k": "v"}},
	}, src)
}

func TestExactCycleRefused(t *testing.T) {
	t.Parallel()

	cyclic := map[string]any{}
	cyclic["self"] = cyclic

	_, ok := jsonvalue.Exact(cyclic)
	assert.False(t, ok)
}

// TestExactDepthCap pins the nesting boundary directly: jsontext's
// 10000-level limit is the depth where the round trip flips from accept to
// refuse, so a toolchain change to that limit trips this test visibly
// instead of silently shifting Exact's contract.
func TestExactDepthCap(t *testing.T) {
	t.Parallel()

	nested := func(depth int) any {
		v := any("leaf")
		for range depth {
			v = []any{v}
		}

		return v
	}

	for depth, want := range map[int]bool{1: true, 9999: true, 10000: true, 10001: false} {
		_, ok := jsonvalue.Exact(nested(depth))
		assert.Equal(t, want, ok, "depth %d", depth)
	}
}

// TestNormalize pins the legacy copy-on-change walk behind the public
// Normalize: an already JSON-shaped container comes back as the same
// container, a changed one is a fresh copy sharing its unchanged children,
// and a cycle terminates at its back-edge.
func TestNormalize(t *testing.T) {
	t.Parallel()

	t.Run("unchanged container is returned as is", func(t *testing.T) {
		t.Parallel()

		in := map[string]any{"s": "str", "n": jsonv1.Number("1"), "arr": []any{"x"}}

		got, ok := jsonvalue.Normalize(in).(map[string]any)
		require.True(t, ok)
		assert.True(t, sameMap(in, got))
	})

	t.Run("changed container shares unchanged children", func(t *testing.T) {
		t.Parallel()

		unchanged := map[string]any{"k": "v"}
		in := map[string]any{"raw": 42, "shared": unchanged}

		got, ok := jsonvalue.Normalize(in).(map[string]any)
		require.True(t, ok)
		assert.Equal(t, 42, in["raw"], "input must not be mutated")
		assert.Equal(t, jsonv1.Number("42"), got["raw"])

		gotShared, ok := got["shared"].(map[string]any)
		require.True(t, ok)
		assert.True(t, sameMap(unchanged, gotShared))
	})

	t.Run("slice copy on change", func(t *testing.T) {
		t.Parallel()

		in := []any{[]any{"a"}, 7, float32(2)}

		got, ok := jsonvalue.Normalize(in).([]any)
		require.True(t, ok)
		assert.Equal(t, 7, in[1], "input must not be mutated")
		assert.Equal(t, []any{[]any{"a"}, jsonv1.Number("7"), float64(2)}, got)
	})

	t.Run("unaccepted leaf passes through", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, struct{ X int }{X: 1}, jsonvalue.Normalize(struct{ X int }{X: 1}))
	})

	t.Run("self-referential map terminates", func(t *testing.T) {
		t.Parallel()

		m := map[string]any{"n": 1}
		m["self"] = m

		got, ok := jsonvalue.Normalize(m).(map[string]any)
		require.True(t, ok)
		assert.Equal(t, jsonv1.Number("1"), got["n"])
		assert.Contains(t, got, "self")
	})

	t.Run("reslice is normalized rather than mistaken for a cycle", func(t *testing.T) {
		t.Parallel()

		backing := []any{10, 20}
		in := []any{backing, backing[:1]}

		got, ok := jsonvalue.Normalize(in).([]any)
		require.True(t, ok)
		assert.Equal(t, []any{
			[]any{jsonv1.Number("10"), jsonv1.Number("20")},
			[]any{jsonv1.Number("10")},
		}, got)
	})
}

// sameMap reports whether two maps are the same underlying map value.
func sameMap(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}

	a["__same_probe__"] = struct{}{}
	defer delete(a, "__same_probe__")

	_, ok := b["__same_probe__"]

	return ok
}
