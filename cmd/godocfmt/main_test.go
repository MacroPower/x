package main

import (
	"go/ast"
	"go/doc/comment"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/stringtest"
)

func TestFormatDoc(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		width int
		want  string
	}{
		"simple paragraph": {
			input: "This is a simple paragraph.",
			width: 80,
			want:  "This is a simple paragraph.\n",
		},
		"wraps long sentence": {
			input: "This is a very long line that should wrap when it exceeds the specified width limit.",
			width: 40,
			want: stringtest.JoinLF(
				"This is a very long line that should",
				"wrap when it exceeds the specified width",
				"limit.",
				"",
			),
		},
		"preserves doc link brackets": {
			input: "See [Foo] for details.",
			width: 80,
			want:  "See [Foo] for details.\n",
		},
		"preserves import path brackets": {
			input: "Use [github.com/foo/bar] for this.",
			width: 80,
			want:  "Use [github.com/foo/bar] for this.\n",
		},
		"preserves method link brackets": {
			input: "Call [Type.Method] to do this.",
			width: 80,
			want:  "Call [Type.Method] to do this.\n",
		},
		"short sentences stay separate": {
			input: "First sentence. Second sentence.",
			width: 80,
			want: stringtest.JoinLF(
				"First sentence.",
				"Second sentence.",
				"",
			),
		},
		// Sentences broken across lines that fit on one line get compacted.
		"compacts short sentence broken across lines": {
			input: stringtest.JoinLF(
				"This is a short",
				"sentence that was",
				"unnecessarily broken.",
			),
			width: 80,
			want:  "This is a short sentence that was unnecessarily broken.\n",
		},
		// First sentence is multi-line but it's first, so no blank before.
		// Second sentence is single-line, so no blank before.
		"multi-line first sentence no padding": {
			input: "This is a longer first sentence that fills more space and wraps. This is the second sentence.",
			width: 50,
			want: stringtest.JoinLF(
				"This is a longer first sentence that fills more",
				"space and wraps.",
				"This is the second sentence.",
				"",
			),
		},
		"code block preserved": {
			input: stringtest.JoinLF(
				"Example:",
				"",
				"\tfoo := bar()",
				"\treturn foo",
				"",
			),
			width: 80,
			want: stringtest.JoinLF(
				"Example:",
				"",
				"\tfoo := bar()",
				"\treturn foo",
				"",
			),
		},
		"heading preserved": {
			input: stringtest.JoinLF(
				"# Section Title",
				"",
				"Paragraph text.",
			),
			width: 80,
			want: stringtest.JoinLF(
				"# Section Title",
				"",
				"Paragraph text.",
				"",
			),
		},
		"list items": {
			input: stringtest.JoinLF(
				"Options:",
				"",
				"  - First item",
				"  - Second item",
			),
			width: 80,
			want: stringtest.JoinLF(
				"Options:",
				"",
				"  - First item",
				"  - Second item",
				"",
			),
		},
		// Numbered lists (Go doc spec).
		"numbered list": {
			input: stringtest.JoinLF(
				"Steps:",
				"",
				" 1. First step",
				" 2. Second step",
				" 3. Third step",
			),
			width: 80,
			want: stringtest.JoinLF(
				"Steps:",
				"",
				" 1. First step",
				" 2. Second step",
				" 3. Third step",
				"",
			),
		},
		// Multiple paragraphs.
		"multiple paragraphs": {
			input: stringtest.JoinLF(
				"First paragraph with some text.",
				"",
				"Second paragraph with more text.",
			),
			width: 80,
			want: stringtest.JoinLF(
				"First paragraph with some text.",
				"",
				"Second paragraph with more text.",
				"",
			),
		},
		// URL links with definitions.
		"url link with definition": {
			input: stringtest.JoinLF(
				"See [RFC 7159] for the specification.",
				"",
				"[RFC 7159]: https://tools.ietf.org/html/rfc7159",
			),
			width: 80,
			want: stringtest.JoinLF(
				"See [RFC 7159] for the specification.",
				"",
				"[RFC 7159]: https://tools.ietf.org/html/rfc7159",
				"",
			),
		},
		// Doc link with punctuation.
		"doc link followed by punctuation": {
			input: "Returns [ErrNotFound], or nil if successful.",
			width: 80,
			want:  "Returns [ErrNotFound], or nil if successful.\n",
		},
		// Doc link at end of sentence.
		"doc link at sentence end": {
			input: "Any error except [io.EOF] is returned. The buffer may grow.",
			width: 80,
			want: stringtest.JoinLF(
				"Any error except [io.EOF] is returned.",
				"The buffer may grow.",
				"",
			),
		},
		// Multiple headings.
		"multiple headings": {
			input: stringtest.JoinLF(
				"# Overview",
				"",
				"Package provides utilities.",
				"",
				"# Usage",
				"",
				"Call the function.",
			),
			width: 80,
			want: stringtest.JoinLF(
				"# Overview",
				"",
				"Package provides utilities.",
				"",
				"# Usage",
				"",
				"Call the function.",
				"",
			),
		},
		// Mixed content: heading + paragraph + code + list.
		"mixed content": {
			input: stringtest.JoinLF(
				"# Example",
				"",
				"Use like this:",
				"",
				"\tx := New()",
				"\tx.Run()",
				"",
				"Options:",
				"",
				"  - Verbose mode",
				"  - Quiet mode",
			),
			width: 80,
			want: stringtest.JoinLF(
				"# Example",
				"",
				"Use like this:",
				"",
				"\tx := New()",
				"\tx.Run()",
				"",
				"Options:",
				"",
				"  - Verbose mode",
				"  - Quiet mode",
				"",
			),
		},
		// Long list item that wraps.
		"list item wraps": {
			input: stringtest.JoinLF(
				"Options:",
				"",
				"  - This is a very long list item that should wrap to multiple lines when it exceeds the width",
			),
			width: 50,
			want: stringtest.JoinLF(
				"Options:",
				"",
				"  - This is a very long list item that should wrap",
				"    to multiple lines when it exceeds the",
				"    width",
				"",
			),
		},
		// Bullet list with different markers (Go accepts *, +, -, •).
		"bullet with star marker": {
			input: stringtest.JoinLF(
				"Items:",
				"",
				"  * First",
				"  * Second",
			),
			width: 80,
			want: stringtest.JoinLF(
				"Items:",
				"",
				"  - First",
				"  - Second",
				"",
			),
		},
		// Code block with blank lines inside.
		"code block with blank lines": {
			input: stringtest.JoinLF(
				"Example:",
				"",
				"\tfunc main() {",
				"\t\tfmt.Println(\"hello\")",
				"",
				"\t\tfmt.Println(\"world\")",
				"\t}",
			),
			width: 80,
			want: stringtest.JoinLF(
				"Example:",
				"",
				"\tfunc main() {",
				"\t\tfmt.Println(\"hello\")",
				"",
				"\t\tfmt.Println(\"world\")",
				"\t}",
				"",
			),
		},
		// Deprecation notice (just a paragraph starting with Deprecated:).
		"deprecation notice": {
			input: "Deprecated: Use NewFunc instead.",
			width: 80,
			want:  "Deprecated: Use NewFunc instead.\n",
		},
		// Empty input.
		"empty input": {
			input: "",
			width: 80,
			want:  "",
		},
		// Single word.
		"single word": {
			input: "Word",
			width: 80,
			want:  "Word\n",
		},
		// Paragraph that exactly fits width.
		"exact width fit": {
			input: "Exactly twenty chars",
			width: 20,
			want:  "Exactly twenty chars\n",
		},
		// Unicode quotes from backticks.
		"backticks to unicode quotes": {
			input: "Use ``example'' for this.",
			width: 80,
			want:  "Use \u201cexample\u201d for this.\n",
		},
		// PublicSuffixList example from user request.
		"public suffix list example": {
			input: "PublicSuffixList provides the public suffix of a domain. For example:",
			width: 77,
			want: stringtest.JoinLF(
				"PublicSuffixList provides the public suffix of a domain.",
				"For example:",
				"",
			),
		},
		// Multi-line sentences get blank line separators.
		"multi-line sentences with padding": {
			input: "Implementations of PublicSuffixList must be safe for concurrent use by multiple goroutines. An implementation that always returns empty is valid and may be useful for testing but it is not secure.",
			width: 77,
			want: stringtest.JoinLF(
				"Implementations of PublicSuffixList must be safe for concurrent use by",
				"multiple goroutines.",
				"",
				"An implementation that always returns empty is valid and may be useful for",
				"testing but it is not secure.",
				"",
			),
		},
		// Three sentences, first two short, third long.
		// Third sentence is multi-line so gets blank BEFORE it.
		"three sentences mixed length": {
			input: "Short one. Another short. This third sentence is much longer and will definitely need to wrap across multiple lines.",
			width: 50,
			want: stringtest.JoinLF(
				"Short one.",
				"Another short.",
				"",
				"This third sentence is much longer and will",
				"definitely need to wrap across multiple lines.",
				"",
			),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var p comment.Parser

			doc := p.Parse(tc.input)
			got := formatDoc(doc, tc.width)

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIsDirectiveComment(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		comments []string
		want     bool
	}{
		"nolint": {
			comments: []string{"//nolint:errcheck"},
			want:     true,
		},
		"go directive": {
			comments: []string{"//go:generate foo"},
			want:     true,
		},
		"build constraint": {
			comments: []string{"//+build linux"},
			want:     true,
		},
		"go build constraint": {
			comments: []string{"//go:build linux"},
			want:     true,
		},
		"regular comment": {
			comments: []string{"// This is a regular comment"},
			want:     false,
		},
		"doc comment": {
			comments: []string{"// Package foo provides bar."},
			want:     false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cg := &ast.CommentGroup{
				List: make([]*ast.Comment, len(tc.comments)),
			}
			for i, c := range tc.comments {
				cg.List[i] = &ast.Comment{Text: c}
			}

			got := isDirectiveComment(cg)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestProcessFile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		want  string
	}{
		"package doc": {
			input: stringtest.JoinLF(
				"// Package foo provides utilities for working with very long descriptions that should wrap properly.",
				"package foo",
			),
			want: stringtest.JoinLF(
				"// Package foo provides utilities for working with very long descriptions that",
				"// should wrap properly.",
				"package foo",
			),
		},
		"function doc": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// New creates a new instance. It accepts options for configuration.",
				"func New() {}",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// New creates a new instance.",
				"// It accepts options for configuration.",
				"func New() {}",
			),
		},
		"type doc": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// Client represents a connection to the server. Use [New] to create one.",
				"type Client struct{}",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// Client represents a connection to the server.",
				"// Use [New] to create one.",
				"type Client struct{}",
			),
		},
		"method doc": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"type Client struct{}",
				"",
				"// Close closes the connection. It returns an error if the connection is already closed.",
				"func (c *Client) Close() error { return nil }",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"type Client struct{}",
				"",
				"// Close closes the connection.",
				"// It returns an error if the connection is already closed.",
				"func (c *Client) Close() error { return nil }",
			),
		},
		"const doc": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// DefaultTimeout is the default timeout value. Use this when no timeout is specified.",
				"const DefaultTimeout = 30",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// DefaultTimeout is the default timeout value.",
				"// Use this when no timeout is specified.",
				"const DefaultTimeout = 30",
			),
		},
		"var doc": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// ErrNotFound is returned when the item is not found. Check with errors.Is.",
				"var ErrNotFound = errors.New(\"not found\")",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// ErrNotFound is returned when the item is not found.",
				"// Check with errors.Is.",
				"var ErrNotFound = errors.New(\"not found\")",
			),
		},
		"preserves directive comments": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"//go:generate stringer -type=Kind",
				"type Kind int",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"//go:generate stringer -type=Kind",
				"type Kind int",
			),
		},
		"doc with code block": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// New creates a new instance.",
				"//",
				"// Example:",
				"//",
				"//\tc := New()",
				"//\tdefer c.Close()",
				"func New() {}",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// New creates a new instance.",
				"//",
				"// Example:",
				"//",
				"//\tc := New()",
				"//\tdefer c.Close()",
				"func New() {}",
			),
		},
		"doc with list": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// Options:",
				"//",
				"//   - WithTimeout sets timeout",
				"//   - WithRetry enables retry",
				"func New() {}",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// Options:",
				"//",
				"//   - WithTimeout sets timeout",
				"//   - WithRetry enables retry",
				"func New() {}",
			),
		},
		"indented struct field doc": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"type Config struct {",
				"\t// Timeout specifies the maximum duration. Set to zero for no timeout.",
				"\tTimeout int",
				"}",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"type Config struct {",
				"\t// Timeout specifies the maximum duration.",
				"\t// Set to zero for no timeout.",
				"\tTimeout int",
				"}",
			),
		},
		"multiple exports": {
			input: stringtest.JoinLF(
				"// Package foo provides utilities. It is designed for ease of use.",
				"package foo",
				"",
				"// ErrInvalid is returned for invalid input. Check the input and retry.",
				"var ErrInvalid = errors.New(\"invalid\")",
				"",
				"// Client handles connections. Use New to create one.",
				"type Client struct{}",
				"",
				"// New creates a new Client. It accepts functional options.",
				"func New() *Client { return nil }",
			),
			want: stringtest.JoinLF(
				"// Package foo provides utilities.",
				"// It is designed for ease of use.",
				"package foo",
				"",
				"// ErrInvalid is returned for invalid input.",
				"// Check the input and retry.",
				"var ErrInvalid = errors.New(\"invalid\")",
				"",
				"// Client handles connections.",
				"// Use New to create one.",
				"type Client struct{}",
				"",
				"// New creates a new Client.",
				"// It accepts functional options.",
				"func New() *Client { return nil }",
			),
		},
		"no changes needed": {
			input: stringtest.JoinLF(
				"// Package foo provides bar.",
				"package foo",
			),
			want: stringtest.JoinLF(
				"// Package foo provides bar.",
				"package foo",
			),
		},
		"long sentence wraps": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// writeSnapshots writes all enabled snapshot profiles (heap, allocs, goroutine, etc.).",
				"func writeSnapshots() {}",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// writeSnapshots writes all enabled snapshot profiles (heap, allocs, goroutine,",
				"// etc.).",
				"func writeSnapshots() {}",
			),
		},
		"compacts unnecessarily broken sentence": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// This is a short",
				"// sentence that was",
				"// unnecessarily broken.",
				"func Foo() {}",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// This is a short sentence that was unnecessarily broken.",
				"func Foo() {}",
			),
		},
		// First sentence is multi-line (first, no blank before).
		// Second sentence is single-line (no blank before).
		// Third sentence is multi-line (blank BEFORE it).
		"multi-line sentence gets padding": {
			input: stringtest.JoinLF(
				"package foo",
				"",
				"// First sentence is quite long. Second short. Third sentence is quite long and will definitely wrap to the next line for sure yes.",
				"func example() {}",
			),
			want: stringtest.JoinLF(
				"package foo",
				"",
				"// First sentence is quite long.",
				"// Second short.",
				"//",
				"// Third sentence is quite long and will definitely wrap to the next line for",
				"// sure yes.",
				"func example() {}",
			),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Create temp file.
			dir := t.TempDir()
			path := filepath.Join(dir, "test.go")
			err := os.WriteFile(path, []byte(tc.input), 0o644)
			require.NoError(t, err)

			// Process file (uses global width=80).
			oldWidth := *width
			*width = 80
			t.Cleanup(func() { *width = oldWidth })

			_, err = processFile(path)
			require.NoError(t, err)

			// Read result.
			got, err := os.ReadFile(path)
			require.NoError(t, err)

			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestProcessFileGolden(t *testing.T) {
	t.Parallel()

	// Read input file.
	inputPath := filepath.Join("testdata", "input_doc.go")
	input, err := os.ReadFile(inputPath)
	require.NoError(t, err)

	// Read expected output.
	wantPath := filepath.Join("testdata", "want_doc.go")
	want, err := os.ReadFile(wantPath)
	require.NoError(t, err)

	// Create temp file with input content.
	dir := t.TempDir()
	testPath := filepath.Join(dir, "test.go")
	err = os.WriteFile(testPath, input, 0o644)
	require.NoError(t, err)

	// Process file with width=80.
	oldWidth := *width
	*width = 80
	t.Cleanup(func() { *width = oldWidth })

	changed, err := processFile(testPath)
	require.NoError(t, err)
	assert.True(t, changed, "expected file to be changed")

	// Read result.
	got, err := os.ReadFile(testPath)
	require.NoError(t, err)

	assert.Equal(t, string(want), string(got))
}

func TestProcessFileGoldenIdempotent(t *testing.T) {
	t.Parallel()

	// Read the already-formatted file.
	wantPath := filepath.Join("testdata", "want_doc.go")
	want, err := os.ReadFile(wantPath)
	require.NoError(t, err)

	// Create temp file with the formatted content.
	dir := t.TempDir()
	testPath := filepath.Join(dir, "test.go")
	err = os.WriteFile(testPath, want, 0o644)
	require.NoError(t, err)

	// Process file with width=80.
	oldWidth := *width
	*width = 80
	t.Cleanup(func() { *width = oldWidth })

	changed, err := processFile(testPath)
	require.NoError(t, err)
	assert.False(t, changed, "expected no changes on already-formatted file")

	// Verify content is unchanged.
	got, err := os.ReadFile(testPath)
	require.NoError(t, err)

	assert.Equal(t, string(want), string(got))
}
