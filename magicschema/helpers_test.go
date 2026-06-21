package magicschema_test

import (
	"math"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/magicschema"
)

func TestSetSchemaType(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		types     []string
		wantType  string
		wantTypes []string
	}{
		"empty leaves the schema untouched": {types: nil},
		"single type collapses to scalar Type": {
			types:    []string{"string"},
			wantType: "string",
		},
		"multiple types become the Types union": {
			types:     []string{"string", "null"},
			wantTypes: []string{"string", "null"},
		},
		"duplicate type collapses to scalar": {
			types:    []string{"string", "string"},
			wantType: "string",
		},
		"duplicates drop while first-seen order is kept": {
			types:     []string{"string", "null", "string"},
			wantTypes: []string{"string", "null"},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Seed both fields to confirm the sibling is always cleared, so a
			// schema never carries Type and Types at once.
			s := &jsonschema.Schema{Type: "seed", Types: []string{"seed"}}
			if tc.types == nil {
				s = &jsonschema.Schema{}
			}

			magicschema.SetSchemaType(s, tc.types)

			assert.Equal(t, tc.wantType, s.Type)
			assert.Equal(t, tc.wantTypes, s.Types)
		})
	}
}

func TestParseYAMLValue(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		val  string
		want string // JSON encoding; empty means nil (no value)
	}{
		"blank carries no value":          {val: "", want: ""},
		"whitespace carries no value":     {val: "   ", want: ""},
		"explicit null parses to null":    {val: "null", want: "null"},
		"string value":                    {val: "hello", want: `"hello"`},
		"integer value keeps native type": {val: "42", want: "42"},
		"boolean value keeps native type": {val: "true", want: "true"},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := magicschema.ParseYAMLValue(tc.val)
			if tc.want == "" {
				assert.Nil(t, got, "a blank value must carry no default")

				return
			}

			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestToSubSchemaArrayAllOrNothing(t *testing.T) {
	t.Parallel()

	// A NaN cannot be JSON-marshaled, so the branch carrying it does not round
	// trip. Dropping just that branch would shrink an anyOf/oneOf and reject
	// values it should accept, so the whole list clears instead.
	got := magicschema.ToSubSchemaArray([]any{
		map[string]any{"type": "string"},
		map[string]any{"const": math.NaN()},
	})
	assert.Nil(t, got, "a branch that cannot round-trip clears the whole combinator")

	// A list whose elements all round-trip is preserved in full.
	got = magicschema.ToSubSchemaArray([]any{
		map[string]any{"type": "string"},
		map[string]any{"type": "integer"},
	})
	assert.Len(t, got, 2)
}
