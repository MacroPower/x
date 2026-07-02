package magicschema_test

import (
	"math"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestToSubSchemaTypeNormalization(t *testing.T) {
	t.Parallel()

	// The nested type arrays a round-tripped sub-schema carries must uphold
	// the same invariants SetSchemaType enforces for annotation-supplied
	// lists: no duplicate members (a JSON Schema type array must have unique
	// members), a single member collapses to the scalar Type, and an empty
	// array leaves the type unset rather than emitting the invalid "type": [].
	tcs := map[string]struct {
		val       any
		wantType  string
		wantTypes []string
	}{
		"null member becomes the null type": {
			val:       map[string]any{"type": []any{nil, "string"}},
			wantTypes: []string{"null", "string"},
		},
		"duplicate members drop while first-seen order is kept": {
			val:       map[string]any{"type": []any{"string", "null", "string"}},
			wantTypes: []string{"string", "null"},
		},
		"null alongside the null string collapses to scalar": {
			val:      map[string]any{"type": []any{"null", nil}},
			wantType: "null",
		},
		"single member collapses to scalar": {
			val:      map[string]any{"type": []any{"string"}},
			wantType: "string",
		},
		"empty type array leaves the type unset": {
			val: map[string]any{"type": []any{}},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Nest the schema under items to confirm the normalization walks
			// the whole tree, not just the top level.
			got := magicschema.ToSubSchema(map[string]any{"items": tc.val})
			require.NotNil(t, got)
			require.NotNil(t, got.Items)

			assert.Equal(t, tc.wantType, got.Items.Type)
			assert.Equal(t, tc.wantTypes, got.Items.Types)
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
