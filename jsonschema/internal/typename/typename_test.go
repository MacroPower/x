package typename_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// TestValid covers the predicate over the seven canonical JSON Schema type
// names, asserting it accepts exactly those and rejects everything else,
// including case variants, near-misses, and surrounding whitespace.
func TestValid(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name string
		want bool
	}{
		"null":    {name: typename.Null, want: true},
		"boolean": {name: typename.Boolean, want: true},
		"string":  {name: typename.String, want: true},
		"integer": {name: typename.Integer, want: true},
		"number":  {name: typename.Number, want: true},
		"object":  {name: typename.Object, want: true},
		"array":   {name: typename.Array, want: true},

		"empty string":          {name: "", want: false},
		"capitalized":           {name: "Null", want: false},
		"plural near-miss":      {name: "integers", want: false},
		"trailing whitespace":   {name: "object ", want: false},
		"unrelated word":        {name: "json", want: false},
		"uppercased whole name": {name: "STRING", want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, typename.Valid(tc.name))
		})
	}
}
