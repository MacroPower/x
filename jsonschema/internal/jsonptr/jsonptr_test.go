package jsonptr_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

func TestSafeToken(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   string
		want string
	}{
		"empty input returns empty":          {in: "", want: ""},
		"unreserved letters pass through":    {in: "AZaz", want: "AZaz"},
		"unreserved digits pass through":     {in: "0123456789", want: "0123456789"},
		"unreserved punctuation unchanged":   {in: "a.b_c-d", want: "a.b_c-d"},
		"mixed unreserved unchanged":         {in: "Box_v1.2-beta", want: "Box_v1.2-beta"},
		"slash maps to underscore":           {in: "a/b", want: "a_b"},
		"tilde maps to underscore":           {in: "a~b", want: "a_b"},
		"brackets map to underscore":         {in: "Box[int]", want: "Box_int_"},
		"spaces map to underscore":           {in: "a b c", want: "a_b_c"},
		"quotes map to underscore":           {in: `a"b'c`, want: "a_b_c"},
		"braces map to underscore":           {in: "struct{A int}", want: "struct_A_int_"},
		"non-ascii maps to underscore":       {in: "café", want: "caf_"},
		"all invalid runes never empty":      {in: "[]/~", want: "____"},
		"comma and asterisk map to under":    {in: "Map[K,V]*", want: "Map_K_V__"},
		"leading and trailing invalid runes": {in: " x ", want: "_x_"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := jsonptr.SafeToken(tc.in)
			assert.Equal(t, tc.want, got)

			// Load-bearing invariant: a non-empty name is never emptied, so a
			// definitions key and its $ref token can never collapse to "".
			if tc.in != "" {
				assert.NotEmpty(t, got)
			}
		})
	}
}

func TestEscapeUnescapeRoundTrip(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in     string
		escape string
	}{
		"no special characters":       {in: "plain", escape: "plain"},
		"tilde escapes to tilde-zero": {in: "a~b", escape: "a~0b"},
		"slash escapes to tilde-one":  {in: "a/b", escape: "a~1b"},
		"both, order preserved":       {in: "~/", escape: "~0~1"},
		"empty":                       {in: "", escape: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := jsonptr.Escape(tc.in)
			assert.Equal(t, tc.escape, got)
			assert.Equal(t, tc.in, jsonptr.Unescape(got))
		})
	}
}

func TestSegmentsKey(t *testing.T) {
	t.Parallel()

	// Distinct segment lists must never share a key, even when their flattened
	// bytes coincide. The classic break is a fixed separator: ["a\x00b"] and
	// ["a", "b"] flatten to the same bytes under a NUL join, so length-prefixing
	// is what keeps them apart.
	lists := [][]string{
		{},
		{""},
		{"", ""},
		{"a"},
		{"a", "b"},
		{"ab"},
		{"a\x00b"},
		{"a", "", "b"},
		{"10", "x"},
		{"1", "0x"},
	}

	seen := map[string][]string{}
	for _, list := range lists {
		key := jsonptr.SegmentsKey(list)

		if prev, ok := seen[key]; ok {
			assert.Equal(t, prev, list, "distinct segment lists share key %q", key)
		}

		seen[key] = list

		// The same list always yields the same key.
		assert.Equal(t, key, jsonptr.SegmentsKey(list))
	}

	// Explicit regression for the NUL-vs-split collision.
	assert.NotEqual(t,
		jsonptr.SegmentsKey([]string{"a\x00b"}),
		jsonptr.SegmentsKey([]string{"a", "b"}),
	)
}
