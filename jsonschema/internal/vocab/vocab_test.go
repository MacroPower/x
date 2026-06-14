package vocab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/vocab"
)

// CheckUnknown is exercised directly; how the parent package surfaces its
// result as ErrUnknownVocabulary is covered by the parent's own tests.

func TestCheckUnknown(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		vocabs map[string]bool
		want   string
	}{
		"all known": {
			vocabs: map[string]bool{
				vocab.Core2020:       true,
				vocab.Validation2020: true,
			},
			want: "",
		},
		"unknown but optional": {
			vocabs: map[string]bool{
				vocab.Core2020:            true,
				"urn:example:extra-vocab": false,
			},
			want: "",
		},
		"single unknown required": {
			vocabs: map[string]bool{
				vocab.Core2020:            true,
				"urn:example:extra-vocab": true,
			},
			want: "urn:example:extra-vocab",
		},
		"multiple unknown required returns the smallest": {
			vocabs: map[string]bool{
				vocab.Core2020:    true,
				"urn:example:zzz": true,
				"urn:example:aaa": true,
				"urn:example:mmm": true,
			},
			want: "urn:example:aaa",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Run repeatedly: a map-iteration-order dependence would surface as
			// an occasional mismatch across the randomized iteration seeds.
			for range 100 {
				assert.Equal(t, tc.want, vocab.CheckUnknown(tc.vocabs))
			}
		})
	}
}
