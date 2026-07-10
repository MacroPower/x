package yamldoc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/magicschema/internal/yamldoc"
)

func TestMaskBlockScalars(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  []string
	}{
		"literal scalar interior blanked": {
			input: "script: |\n  # fake comment\n  ## @param x y\nreal: 1\n",
			want:  []string{"script: |", "", "", "real: 1", ""},
		},
		"folded scalar with chomping and comment": {
			input: "text: >- # c\n  data line\nnext: 2\n",
			want:  []string{"text: >- # c", "", "next: 2", ""},
		},
		"sequence item scalar": {
			input: "list:\n  - |\n    # data\n  - plain\n",
			want:  []string{"list:", "  - |", "", "  - plain", ""},
		},
		"blank lines stay inside the scalar": {
			input: "s: |\n  a\n\n  b\nreal: 1\n",
			want:  []string{"s: |", "", "", "", "real: 1", ""},
		},
		"comment ending in an indicator opens nothing": {
			input: "# usage: |\n  ## @param x y\nreal: 1\n",
			want:  []string{"# usage: |", "  ## @param x y", "real: 1", ""},
		},
		"plain scalar containing a pipe opens nothing": {
			input: "cmd: foo | bar\n  # not data\n",
			want:  []string{"cmd: foo | bar", "  # not data", ""},
		},
		"no block scalars unchanged": {
			input: "a: 1\n# comment\nb: 2\n",
			want:  []string{"a: 1", "# comment", "b: 2", ""},
		},
		"anchored scalar header masks its interior": {
			input: "script: &tpl |\n  ## @param x y\nreal: 1\n",
			want:  []string{"script: &tpl |", "", "real: 1", ""},
		},
		"tagged scalar header masks its interior": {
			input: "s: !!str |\n  # data\nreal: 1\n",
			want:  []string{"s: !!str |", "", "real: 1", ""},
		},
		"anchor and tag together mask their interior": {
			input: "s: &a !!str >-\n  # data\nreal: 1\n",
			want:  []string{"s: &a !!str >-", "", "real: 1", ""},
		},
		"indicator inside a trailing comment opens nothing": {
			// The '#' is preceded by whitespace, so everything after it is a
			// comment; the indicator-like suffix must not mask the following
			// indented lines.
			input: "image: # config: |\n  # image.tag doc\n  tag: latest\n",
			want:  []string{"image: # config: |", "  # image.tag doc", "  tag: latest", ""},
		},
		"sequence entry mapping sibling stays unmasked": {
			// The scalar's owner is the mapping key past the dash, so the
			// entry's sibling key (and its annotation comment) sits at the
			// owner's indent and ends the scalar rather than being masked.
			input: "items:\n  - script: |\n      echo hi\n    ## @param items.other x\n    other: value\n",
			want: []string{
				"items:", "  - script: |", "",
				"    ## @param items.other x", "    other: value", "",
			},
		},
		"entry-owned anchored scalar masks only its interior": {
			// With the indicator (behind its anchor) directly on the entry,
			// the dash itself owns the scalar: interior lines sit past the
			// dash and the next entry at the dash's column ends it.
			input: "list:\n  - &a |\n    data\n  - plain\n",
			want:  []string{"list:", "  - &a |", "", "  - plain", ""},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := yamldoc.MaskBlockScalars([]byte(tc.input))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDropEmptyDocuments(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  string
	}{
		"bare separator before bare separator collapses": {
			// The dropped separator blanks in place rather than deleting its
			// line, so later lines keep their physical line numbers and
			// parser positions still point at the user's file.
			input: "a: 1\n---\n\n---\nb: 2\n",
			want:  "a: 1\n\n\n---\nb: 2\n",
		},
		"comment-carrying separator before bare separator collapses": {
			input: "a: 1\n--- # c\n\n---\nb: 2\n",
			want:  "a: 1\n\n\n---\nb: 2\n",
		},
		"bare separator before comment-carrying separator collapses": {
			input: "a: 1\n---\n\n--- # c\nb: 2\n",
			want:  "a: 1\n\n\n--- # c\nb: 2\n",
		},
		"tab before trailing comment is still bare": {
			input: "a: 1\n---\t# c\n\n---\nb: 2\n",
			want:  "a: 1\n\n\n---\nb: 2\n",
		},
		"bare separator before content-carrying start collapses": {
			input: "a: 1\n---\n--- {b: 2}\n",
			want:  "a: 1\n\n--- {b: 2}\n",
		},
		"bare separator before content-carrying start across blanks collapses": {
			input: "a: 1\n---\n\n--- {b: 2}\n",
			want:  "a: 1\n\n\n--- {b: 2}\n",
		},
		"comment-carrying separator opening a non-empty document is kept": {
			input: "a: 1\n--- # c\nb: 2\n",
			want:  "a: 1\n--- # c\nb: 2\n",
		},
		"fused comment is a plain scalar, not a separator": {
			input: "a: 1\n---#c\n\n---\nb: 2\n",
			want:  "a: 1\n---#c\n\n---\nb: 2\n",
		},
		"end marker is not a collapse target": {
			input: "a: 1\n---\n\n...\nb: 2\n",
			want:  "a: 1\n---\n\n...\nb: 2\n",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := yamldoc.DropEmptyDocuments([]byte(tc.input))

			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestSplitDocumentBytes(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  []string
	}{
		"two documents separated by ---": {
			input: "a: 1\n---\nb: 2\n",
			want:  []string{"a: 1", "b: 2\n"},
		},
		"leading separator drops the blank opener": {
			input: "---\na: 1\n",
			want:  []string{"a: 1\n"},
		},
		"empty document between ... markers is dropped": {
			// "..." is not collapsed by DropEmptyDocuments, so the empty middle
			// document reaches the split. The parser emits no document for it,
			// so dropping the blank segment keeps the count aligned with
			// file.Docs instead of forcing the whole-stream fallback.
			input: "a: 1\n...\n\n...\nb: 2\n",
			want:  []string{"a: 1", "b: 2\n"},
		},
		"end marker separates two documents": {
			input: "a: 1\n...\nb: 2\n",
			want:  []string{"a: 1", "b: 2\n"},
		},
		"separator with trailing comment separates two documents": {
			input: "a: 1\n--- # c\nb: 2\n",
			want:  []string{"a: 1", "b: 2\n"},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := yamldoc.SplitDocumentBytes([]byte(tc.input))

			gotStr := make([]string, len(got))
			for i, seg := range got {
				gotStr[i] = string(seg)
			}

			assert.Equal(t, tc.want, gotStr)
		})
	}
}
