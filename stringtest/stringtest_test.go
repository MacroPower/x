package stringtest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/stringtest"
)

func TestInput(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  string
	}{
		"empty string": {
			input: "",
			want:  "",
		},
		"single line no indent": {
			input: "hello",
			want:  "hello",
		},
		"single line with leading newline": {
			input: "\nhello",
			want:  "hello",
		},
		"single line with trailing newline": {
			input: "hello\n",
			want:  "hello",
		},
		"single line with both newlines": {
			input: "\nhello\n",
			want:  "hello",
		},
		"multi-line no indent": {
			input: "line1\nline2\nline3",
			want:  "line1\nline2\nline3",
		},
		"multi-line with common indent spaces": {
			input: `
    line1
    line2
    line3`,
			want: "line1\nline2\nline3",
		},
		"multi-line with common indent tabs": {
			input: "\n\tline1\n\tline2\n\tline3",
			want:  "line1\nline2\nline3",
		},
		"multi-line with varying indent": {
			input: `
    line1
      indented
    line3`,
			want: "line1\n  indented\nline3",
		},
		"multi-line with empty lines": {
			input: `
    line1

    line3`,
			want: "line1\n\nline3",
		},
		"multi-line with whitespace-only lines": {
			input: "\n    line1\n    \n    line3",
			want:  "line1\n\nline3",
		},
		"preserves multiple leading newlines minus one": {
			input: "\n\nline1\nline2",
			want:  "\nline1\nline2",
		},
		"preserves multiple trailing newlines minus one": {
			input: "line1\nline2\n\n",
			want:  "line1\nline2\n",
		},
		"yaml-like input": {
			input: `
    key: value
    nested:
      child: data
    list:
      - item1
      - item2`,
			want: "key: value\nnested:\n  child: data\nlist:\n  - item1\n  - item2",
		},
		"already dedented": {
			input: "key: value\nnested:\n  child: data",
			want:  "key: value\nnested:\n  child: data",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := stringtest.Input(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestJoinLF(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		want  string
		input []string
	}{
		"empty input": {
			input: nil,
			want:  "",
		},
		"single string": {
			input: []string{"hello"},
			want:  "hello",
		},
		"two strings": {
			input: []string{"a", "b"},
			want:  "a\nb",
		},
		"three strings": {
			input: []string{"line1", "line2", "line3"},
			want:  "line1\nline2\nline3",
		},
		"with empty string": {
			input: []string{"a", "", "c"},
			want:  "a\n\nc",
		},
		"already contains newlines": {
			input: []string{"a\nb", "c"},
			want:  "a\nb\nc",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := stringtest.JoinLF(tc.input...)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestJoinCRLF(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		want  string
		input []string
	}{
		"empty input": {
			input: nil,
			want:  "",
		},
		"single string": {
			input: []string{"hello"},
			want:  "hello",
		},
		"two strings": {
			input: []string{"a", "b"},
			want:  "a\r\nb",
		},
		"three strings": {
			input: []string{"line1", "line2", "line3"},
			want:  "line1\r\nline2\r\nline3",
		},
		"with empty string": {
			input: []string{"a", "", "c"},
			want:  "a\r\n\r\nc",
		},
		"already contains newlines": {
			input: []string{"a\nb", "c"},
			want:  "a\nb\r\nc",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := stringtest.JoinCRLF(tc.input...)
			assert.Equal(t, tc.want, got)
		})
	}
}
