package magicschema_test

import (
	"encoding/json"
	"math"
	"slices"
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
		"empty-string member becomes the null type": {
			types:     []string{"", "string"},
			wantTypes: []string{"null", "string"},
		},
		"empty-string member alone collapses to scalar null": {
			types:    []string{""},
			wantType: "null",
		},
		"empty-string member dedups against the null type": {
			types:    []string{"", "null"},
			wantType: "null",
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

			input := slices.Clone(tc.types)

			magicschema.SetSchemaType(s, tc.types)

			assert.Equal(t, tc.wantType, s.Type)
			assert.Equal(t, tc.wantTypes, s.Types)
			assert.Equal(t, input, tc.types, "the caller's slice must stay untouched")
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

// TestLastCommentGroup covers the group contract: a "#"-only line (or an
// empty line, its marker-stripped form) is a paragraph separator within one
// comment group, not a group delimiter -- upstream annotation formats delimit
// only on physical blank lines, which [magicschema.HeadCommentRun] handles --
// so interior separators are preserved and only the blank edges are trimmed.
func TestLastCommentGroup(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		lines []string
		want  []string
	}{
		"nil input": {},
		"all blank lines trim to nothing": {
			lines: []string{"#", "  #  ", ""},
		},
		"interior hash-only line is kept as a separator": {
			lines: []string{"# @schema type:integer;minimum:1", "#", "# The number of replicas."},
			want:  []string{"# @schema type:integer;minimum:1", "#", "# The number of replicas."},
		},
		"interior empty line is kept as a separator": {
			lines: []string{"First paragraph.", "", "Second paragraph."},
			want:  []string{"First paragraph.", "", "Second paragraph."},
		},
		"leading blank lines are trimmed": {
			lines: []string{"#", "# Description"},
			want:  []string{"# Description"},
		},
		"trailing blank lines are trimmed": {
			lines: []string{"# Description", "#", "  # "},
			want:  []string{"# Description"},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := magicschema.LastCommentGroup(tc.lines)
			if tc.want == nil {
				assert.Empty(t, got)

				return
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// TestClassifySchemaLine covers the @schema / @schema.root delimiter grammar
// shared by the structural fence tracking and the helm-schema annotator's
// comment scan: junk suffixes such as "@schema@" still delimit (upstream
// helm-schema toggles on any "# @schema"-prefixed line), a
// whitespace-separated suffix is the helm-values-schema inline form, and
// "@schema.root" must be contiguous and bare to delimit a root block.
func TestClassifySchemaLine(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		line string
		want magicschema.SchemaLineKind
	}{
		"prose is plain": {
			line: "The number of replicas.",
			want: magicschema.SchemaLinePlain,
		},
		"empty line is plain": {
			line: "",
			want: magicschema.SchemaLinePlain,
		},
		"bare @schema delimits": {
			line: "@schema",
			want: magicschema.SchemaLineSchema,
		},
		"junk suffix still delimits": {
			line: "@schema@",
			want: magicschema.SchemaLineSchema,
		},
		"dotted junk suffix still delimits": {
			line: "@schema.foo",
			want: magicschema.SchemaLineSchema,
		},
		"space-separated suffix is the inline form": {
			line: "@schema type:integer;minimum:1",
			want: magicschema.SchemaLineInline,
		},
		"tab-separated suffix is the inline form": {
			line: "@schema\ttype:integer",
			want: magicschema.SchemaLineInline,
		},
		"spaced .root is the inline form": {
			line: "@schema .root",
			want: magicschema.SchemaLineInline,
		},
		"bare @schema.root delimits": {
			line: "@schema.root",
			want: magicschema.SchemaLineRoot,
		},
		"@schema.root with trailing content is inline": {
			line: "@schema.root title: Values",
			want: magicschema.SchemaLineInline,
		},
		"@schema.root with attached junk is inline": {
			line: "@schema.root@",
			want: magicschema.SchemaLineInline,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, magicschema.ClassifySchemaLine(tc.line))
		})
	}
}

// TestIsMarkerBoundary covers the whole-token boundary every marker scan
// shares: only end-of-line, a space, or a tab after the marker counts, so a
// longer word ("@schemafoo") or punctuation ("@schema@") is not the marker.
func TestIsMarkerBoundary(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		rest string
		want bool
	}{
		"empty rest bounds the marker":   {rest: "", want: true},
		"leading space bounds":           {rest: " type:string", want: true},
		"leading tab bounds":             {rest: "\ttype:string", want: true},
		"attached word does not bound":   {rest: "foo", want: false},
		"punctuation does not bound":     {rest: "@", want: false},
		"leading newline does not bound": {rest: "\nfoo", want: false},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, magicschema.IsMarkerBoundary(tc.rest))
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

func TestToSubSchemaMarshalValidation(t *testing.T) {
	t.Parallel()

	// The jsonschema marshaler validates its invariants only on marshal, so a
	// decoded schema can carry a combination the final document marshal
	// rejects -- both definitions and $defs. Every nested sub-schema is
	// checked during a document marshal, so passing one through would turn a
	// single bad annotation into a fatal marshal of the whole output.
	// ToSubSchema returns nil for such values instead (fail open).
	defs := map[string]any{"a": map[string]any{"type": "string"}}

	tcs := map[string]struct {
		val  any
		want bool // whether a schema is returned
	}{
		"both definitions and $defs at the top level are skipped": {
			val: map[string]any{"definitions": defs, "$defs": defs},
		},
		"both definitions and $defs in a nested carrier are skipped": {
			val: map[string]any{
				"items": map[string]any{"definitions": defs, "$defs": defs},
			},
		},
		"a single defs shape survives": {
			val:  map[string]any{"$defs": defs},
			want: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := magicschema.ToSubSchema(tc.val)
			if !tc.want {
				assert.Nil(t, got, "a schema the marshaler rejects must be skipped")

				return
			}

			require.NotNil(t, got)

			_, err := json.Marshal(got)
			require.NoError(t, err, "a returned sub-schema must marshal cleanly")
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

func TestToSubSchemaMapNilWhenNothingSurvives(t *testing.T) {
	t.Parallel()

	// Individual bad entries drop, but an all-bad (or empty) map returns nil
	// rather than an empty map: callers gate on nil, and a non-nil empty map
	// would win a definitions/$defs precedence contest with no content or
	// suppress the structural property fill.
	assert.Nil(t, magicschema.ToSubSchemaMap(map[string]any{"bad": 7}))
	assert.Nil(t, magicschema.ToSubSchemaMap(map[string]any{}))

	got := magicschema.ToSubSchemaMap(map[string]any{
		"bad":  7,
		"good": map[string]any{"type": "string"},
	})
	require.Len(t, got, 1)
	assert.Contains(t, got, "good")
}
