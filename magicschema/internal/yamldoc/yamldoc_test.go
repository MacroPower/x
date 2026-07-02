package yamldoc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/magicschema/internal/yamldoc"
)

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
