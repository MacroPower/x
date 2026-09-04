package normalize_test

import (
	"encoding/json/jsontext"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
)

// TestExactValue pins ExactValue's outputs as literals: the exact integer
// text above 2^53, the v2 encoder's shortest-decimal float forms (float32's
// 32-bit one included), the negative-zero sign, container rebuilds, and the
// refusals. Literals rather than an oracle on purpose -- ExactValue IS the
// marshal + exact-decode round trip, so an oracle spelled the same way would
// compare the implementation to itself and pin nothing.
func TestExactValue(t *testing.T) {
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
		"int8":                    {in: int8(-8), want: jsonv1.Number("-8"), ok: true},
		"int16":                   {in: int16(-16), want: jsonv1.Number("-16"), ok: true},
		"int32":                   {in: int32(-32), want: jsonv1.Number("-32"), ok: true},
		"int64 large":             {in: int64(1)<<62 + 1, want: jsonv1.Number("4611686018427387905"), ok: true},
		"uint":                    {in: uint(42), want: jsonv1.Number("42"), ok: true},
		"uint8":                   {in: uint8(8), want: jsonv1.Number("8"), ok: true},
		"uint16":                  {in: uint16(16), want: jsonv1.Number("16"), ok: true},
		"uint32":                  {in: uint32(32), want: jsonv1.Number("32"), ok: true},
		"uint64 above 2^53":       {in: uint64(1)<<60 + 1, want: jsonv1.Number("1152921504606846977"), ok: true},
		"uintptr":                 {in: uintptr(7), want: jsonv1.Number("7"), ok: true},
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
		"named string fallback": {in: struct {
			S customString
		}{S: "x"}.S, want: "x", ok: true},
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

			got, ok := normalize.ExactValue(tt.in)

			require.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

type customString string

func TestExactValueUnaliased(t *testing.T) {
	t.Parallel()

	src := map[string]any{
		"list": []any{jsonv1.Number("1"), map[string]any{"k": "v"}},
	}

	got, ok := normalize.ExactValue(src)
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

func TestExactValueCycleRefused(t *testing.T) {
	t.Parallel()

	cyclic := map[string]any{}
	cyclic["self"] = cyclic

	_, ok := normalize.ExactValue(cyclic)
	assert.False(t, ok)
}

// TestExactValueDepthCap pins the nesting boundary directly: jsontext's
// 10000-level limit is the depth where the round trip flips from accept to
// refuse, so a toolchain change to that limit trips this test visibly
// instead of silently shifting ExactValue's contract.
func TestExactValueDepthCap(t *testing.T) {
	t.Parallel()

	nested := func(depth int) any {
		v := any("leaf")
		for range depth {
			v = []any{v}
		}

		return v
	}

	for depth, want := range map[int]bool{1: true, 9999: true, 10000: true, 10001: false} {
		_, ok := normalize.ExactValue(nested(depth))
		assert.Equal(t, want, ok, "depth %d", depth)
	}
}
