package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// containerNullPhrases are the tag-literal sentences on which container
// occurrences admit a null literal. Each defers to the nullability discussion
// rather than restating a "never null" law that WithJSONOptions makes false,
// and both doc.go and README.md must carry each one verbatim.
var containerNullPhrases = map[string]string{
	"container literal": "takes the literal only where its occurrence admits null",
	"default options":   "Under the default marshal options none does",
	"json options":      "admits the null, and the literal with it",
}

// TestContainerNullDocumented asserts every phrase appears in both doc.go and
// README.md, over the normalized text the format-deviation ledger compares on.
func TestContainerNullDocumented(t *testing.T) {
	t.Parallel()

	sources := map[string]string{
		"doc.go":    normalizeDeviationProse(t, "doc.go"),
		"README.md": normalizeDeviationProse(t, "README.md"),
	}

	for name, phrase := range containerNullPhrases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for file, prose := range sources {
				require.Contains(t, prose, phrase,
					"%s no longer carries the %s phrase; doc.go and README.md have drifted", file, name)
			}
		})
	}
}
