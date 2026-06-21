package yamldoc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/magicschema/internal/yamldoc"
)

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
