package vocab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/vocab"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		vocabs map[string]bool
		want   vocab.Set
	}{
		"required vocabularies are active": {
			vocabs: map[string]bool{
				vocab.Core2020:       true,
				vocab.Applicator2020: true,
				vocab.Validation2020: true,
			},
			want: vocab.Set{
				Applicator: true,
				Validation: true,
			},
		},
		"absent vocabularies are inactive": {
			vocabs: map[string]bool{
				vocab.Core2020: true,
			},
			want: vocab.Set{},
		},
		"recognized vocabularies declared optional stay active": {
			// Core section 8.1.2: the boolean marks required vs optional for
			// implementations that do not recognize the vocabulary; it has no
			// impact on ones that do. Every group here is recognized, so a
			// false value must still activate it.
			vocabs: map[string]bool{
				vocab.Core2020:            true,
				vocab.Applicator2020:      false,
				vocab.Validation2020:      false,
				vocab.Unevaluated2020:     false,
				vocab.Content2020:         false,
				vocab.FormatAssertion2020: false,
			},
			want: vocab.Set{
				Applicator:      true,
				Validation:      true,
				Unevaluated:     true,
				Content:         true,
				FormatAssertion: true,
			},
		},
		"unrecognized optional vocabularies are ignored": {
			vocabs: map[string]bool{
				vocab.Core2020:            true,
				vocab.Validation2020:      true,
				"urn:example:extra-vocab": false,
			},
			want: vocab.Set{
				Validation: true,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, vocab.Resolve(tc.vocabs))
		})
	}
}
