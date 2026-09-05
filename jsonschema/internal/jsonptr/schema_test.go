package jsonptr_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

// materializeSchema is a plain JSON round-trip [jsonptr.Materialize] for tests;
// the production materializer (the parent's ParseSchemaValue) additionally
// keeps const/enum numbers exact, which these structural tests do not exercise.
func materializeSchema(node any) (*jsonschema.Schema, error) {
	data, err := json.Marshal(node)
	if err != nil {
		return nil, err
	}

	var s jsonschema.Schema

	err = json.Unmarshal(data, &s)
	if err != nil {
		return nil, err
	}

	return &s, nil
}

// TestSchemaAtJSONForm pins the JSON-form walk: which segments locate a
// schema, which locate nothing, and which crossed $id rebases the base URI.
func TestSchemaAtJSONForm(t *testing.T) {
	t.Parallel()

	root := &jsonschema.Schema{
		ID:   "https://example.com/root",
		Type: "object",
		Defs: map[string]*jsonschema.Schema{
			"Foo": {Type: "string"},
		},
		PrefixItems: []*jsonschema.Schema{
			{Type: "integer"},
			{Type: "boolean"},
		},
	}

	idRoot := &jsonschema.Schema{
		ID: "https://example.com/root",
		Defs: map[string]*jsonschema.Schema{
			"Sub": {
				ID:         "sub/",
				Properties: map[string]*jsonschema.Schema{"p": {Type: "string"}},
			},
			"Frag": {
				ID:         "#anchored",
				Properties: map[string]*jsonschema.Schema{"p": {Type: "number"}},
			},
		},
	}

	tests := map[string]struct {
		root     *jsonschema.Schema
		segs     []string
		base     string
		trackIDs bool
		want     *jsonschema.Schema
		wantBase string
	}{
		"empty segments locate the schema itself": {
			root: root, base: "https://example.com/root", trackIDs: true,
			want: root, wantBase: "https://example.com/root",
		},
		"navigates into $defs": {
			root: root, segs: []string{"$defs", "Foo"}, base: "https://example.com/root", trackIDs: true,
			want: &jsonschema.Schema{Type: "string"}, wantBase: "https://example.com/root",
		},
		"navigates an array index": {
			root: root, segs: []string{"prefixItems", "1"}, trackIDs: true,
			want: &jsonschema.Schema{Type: "boolean"},
		},
		"missing segment returns nil": {
			root: root, segs: []string{"$defs", "Missing"}, trackIDs: true,
		},
		"non-schema target returns nil": {
			root: root, segs: []string{"type"}, trackIDs: true,
		},
		"non-canonical index returns nil": {
			root: root, segs: []string{"prefixItems", "01"}, trackIDs: true,
		},
		"crossed $id rebases the base": {
			root: idRoot, segs: []string{"$defs", "Sub", "properties", "p"},
			base: "https://example.com/root", trackIDs: true,
			want: &jsonschema.Schema{Type: "string"}, wantBase: "https://example.com/sub/",
		},
		"crossed $id is inert without tracking": {
			root: idRoot, segs: []string{"$defs", "Sub", "properties", "p"},
			base: "https://example.com/root",
			want: &jsonschema.Schema{Type: "string"}, wantBase: "https://example.com/root",
		},
		"fragment-only $id leaves the base": {
			root: idRoot, segs: []string{"$defs", "Frag", "properties", "p"},
			base: "https://example.com/root", trackIDs: true,
			want: &jsonschema.Schema{Type: "number"}, wantBase: "https://example.com/root",
		},
		"the schema's own $id never rebases": {
			root: idRoot, segs: []string{"$defs", "Frag"},
			base: "https://example.com/other", trackIDs: true,
			want: idRoot.Defs["Frag"], wantBase: "https://example.com/other",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, base := jsonptr.SchemaAtJSONForm(tt.root, tt.segs, tt.base, tt.trackIDs, materializeSchema)

			if tt.want == nil {
				assert.Nil(t, got)
				assert.Empty(t, base)

				return
			}

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantBase, base)
		})
	}
}
