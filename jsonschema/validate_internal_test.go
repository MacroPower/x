package jsonschema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCompileRecordsRefBearing pins the flag that gates the run's cycle set:
// a graph with no reference leaves it unset, a $ref or an active $dynamicRef
// anywhere in the indexed graph sets it, and a $dynamicRef under Draft-07,
// where the keyword never evaluates, does not. The rows' compile steps are
// the record point, so a reference in a compile-fetched document counts too.
func TestCompileRecordsRefBearing(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		opts   []ValidateOption
		want   bool
	}{
		"reference-free root": {
			schema: `{"type": "object", "properties": {"a": {"type": "string"}}}`,
		},
		"$ref": {
			schema: `{"$ref": "#/$defs/a", "$defs": {"a": {"type": "string"}}}`,
			want:   true,
		},
		"$dynamicRef under 2020-12": {
			schema: `{"$dynamicRef": "#a", "$defs": {"a": {"$dynamicAnchor": "a", "type": "string"}}}`,
			want:   true,
		},
		"$dynamicRef under Draft-07": {
			schema: `{"$dynamicRef": "#a", "definitions": {"a": {"type": "string"}}}`,
			opts:   []ValidateOption{WithDraft(Draft7)},
		},
		"nested Draft-07 $ref": {
			schema: `{"properties": {"a": {"$ref": "#/definitions/a"}}, "definitions": {"a": {"type": "string"}}}`,
			opts:   []ValidateOption{WithDraft(Draft7)},
			want:   true,
		},
		"reference only to a remote": {
			schema: `{"$ref": "http://x/a"}`,
			opts: []ValidateOption{WithRefResolver(SchemaMap{
				"http://x/a": {Type: "string"},
			})},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := CompileJSON(t.Context(), []byte(tc.schema), tc.opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, v.proto.refBearing)

			run := v.proto.forInstance(t.Context())
			require.Equal(t, tc.want, run.visiting != nil,
				"the run allocates the cycle set exactly on a ref-bearing graph")
		})
	}
}
