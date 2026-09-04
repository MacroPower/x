package normalize_test

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
)

// panicMarshaler is a value whose own marshaler panics, which DocumentValue
// must report as no JSON form rather than let escape.
type panicMarshaler struct{}

func (panicMarshaler) MarshalJSON() ([]byte, error) {
	panic("marshal")
}

// TestDocumentValue pins the encoding/json v1 rendering DocumentValue takes:
// a typed nil container or pointer is null, an empty non-nil container keeps
// its shape, a struct becomes an object, a raw value decodes to what it
// holds, and a string v1 escapes comes back unescaped. The four accepted
// shapes that still take the render come back as v1 writes them: a float32
// as its 32-bit shortest decimal, an empty Number as 0, and invalid UTF-8 in
// a string or member name as U+FFFD, each at the top level and nested. The
// refusals cover a value with no JSON form and a marshaler that panics.
func TestDocumentValue(t *testing.T) {
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
		"invalid UTF-8 string":        {in: "\xff", want: "\ufffd", ok: true},
		"nested invalid UTF-8 string": {in: []any{"a", "\xff"}, want: []any{"a", "\ufffd"}, ok: true},
		"invalid UTF-8 member name": {
			in:   map[string]any{"\xff": 1},
			want: map[string]any{"\ufffd": jsonv1.Number("1")},
			ok:   true,
		},
		"nested invalid UTF-8 member name": {
			in:   []any{map[string]any{"k\xff": "v"}},
			want: []any{map[string]any{"k\ufffd": "v"}},
			ok:   true,
		},
		"chan":                {in: make(chan int), want: nil, ok: false},
		"func":                {in: func() {}, want: nil, ok: false},
		"panicking marshaler": {in: panicMarshaler{}, want: nil, ok: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := normalize.DocumentValue(tt.in)
			require.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestDocumentValueKeepsAcceptedValue pins the fast path: a value ValueChecked
// accepts comes back as the same container rather than a rebuilt copy.
func TestDocumentValueKeepsAcceptedValue(t *testing.T) {
	t.Parallel()

	in := map[string]any{"k": []any{"v"}}

	got, ok := normalize.DocumentValue(in)
	require.True(t, ok)

	m, isMap := got.(map[string]any)
	require.True(t, isMap)

	in["k"] = "changed"

	assert.Equal(t, "changed", m["k"])
}
