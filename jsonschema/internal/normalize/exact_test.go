package normalize_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
)

// exactValueOracle is the round trip [normalize.ExactValue] mirrors: marshal
// with encoding/json/v2 and decode with the exact-number discipline. The
// tests assert against it rather than against hardcoded literals, so the
// parity claim is pinned to the encoder itself.
func exactValueOracle(t *testing.T, v any) (any, bool) {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}

	out, err := normalize.DecodeJSONInstance(data)
	if err != nil {
		return nil, false
	}

	return out, true
}

func TestExactValue(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in any
	}{
		"nil":                     {in: nil},
		"bool":                    {in: true},
		"string":                  {in: "hello"},
		"number literal":          {in: jsonv1.Number("9007199254740993")},
		"number exponent literal": {in: jsonv1.Number("1e2")},
		"float64 fraction":        {in: 0.1},
		"float64 negative zero":   {in: math.Copysign(0, -1)},
		"float64 large":           {in: 1e21},
		"float64 small":           {in: 1e-9},
		"float64 above 2^53":      {in: float64(1 << 60)},
		"float32 fraction":        {in: float32(0.1)},
		"int":                     {in: -42},
		"int8":                    {in: int8(-8)},
		"int16":                   {in: int16(-16)},
		"int32":                   {in: int32(-32)},
		"int64 large":             {in: int64(1)<<62 + 1},
		"uint":                    {in: uint(42)},
		"uint8":                   {in: uint8(8)},
		"uint16":                  {in: uint16(16)},
		"uint32":                  {in: uint32(32)},
		"uint64 above 2^53":       {in: uint64(1)<<60 + 1},
		"uintptr":                 {in: uintptr(7)},
		"nested containers": {in: map[string]any{
			"list": []any{jsonv1.Number("1"), 0.5, "x", nil},
			"map":  map[string]any{"k": uint64(1)<<60 + 1},
		}},
		"empty map":            {in: map[string]any{}},
		"nil map":              {in: map[string]any(nil)},
		"empty slice":          {in: []any{}},
		"nil slice":            {in: []any(nil)},
		"struct fallback":      {in: struct{ A int }{A: 1}},
		"raw message fallback": {in: jsonv1.RawMessage(`{"n":9007199254740993}`)},
		"raw value fallback":   {in: jsontext.Value(`[1,2]`)},
		"named string fallback": {in: struct {
			S customString
		}{S: "x"}.S},
		"invalid number literal": {in: jsonv1.Number("abc")},
		"empty number literal":   {in: jsonv1.Number("")},
		"nan":                    {in: math.NaN()},
		"positive infinity":      {in: math.Inf(1)},
		"invalid utf8 string":    {in: "a\xffb"},
		"invalid utf8 map key":   {in: map[string]any{"a\xffb": 1}},
		"unmarshalable fallback": {in: make(chan int)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			want, wantOK := exactValueOracle(t, tt.in)
			got, gotOK := normalize.ExactValue(tt.in)

			require.Equal(t, wantOK, gotOK)
			assert.Equal(t, want, got)
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

// TestExactValueDepthMirrorsMarshal pins the depth cap to the mirrored round
// trip around the boundary: ExactValue's accept/refuse verdict must agree
// with the real marshal+decode outcome at each probed depth, so a toolchain
// change to jsontext's nesting limit trips this test instead of silently
// splitting the two.
func TestExactValueDepthMirrorsMarshal(t *testing.T) {
	t.Parallel()

	nested := func(depth int) any {
		v := any("leaf")
		for range depth {
			v = []any{v}
		}

		return v
	}

	for _, depth := range []int{1, 9999, 10000, 10001} {
		in := nested(depth)

		_, wantOK := exactValueOracle(t, in)
		_, gotOK := normalize.ExactValue(in)

		assert.Equal(t, wantOK, gotOK, "depth %d", depth)
	}
}
