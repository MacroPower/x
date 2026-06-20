package schemashape_test

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

func TestHasRefSiblings(t *testing.T) {
	t.Parallel()

	// Each case starts from a bare $ref and sets exactly one sibling field. Every
	// annotation/identifier field is pinned individually so a future change to
	// the explicit list is caught; the constraint cases exercise the IsEmpty
	// delegation.
	withRef := func(mutate func(*jsonschema.Schema)) *jsonschema.Schema {
		s := &jsonschema.Schema{Ref: "#/$defs/X"}
		mutate(s)

		return s
	}

	tests := map[string]struct {
		schema *jsonschema.Schema
		want   bool
	}{
		"bare $ref has no siblings": {schema: withRef(func(*jsonschema.Schema) {}), want: false},

		// Annotation, metadata, and identifier fields: checked explicitly.
		"description": {schema: withRef(func(s *jsonschema.Schema) { s.Description = "d" }), want: true},
		"title":       {schema: withRef(func(s *jsonschema.Schema) { s.Title = "t" }), want: true},
		"default": {
			schema: withRef(func(s *jsonschema.Schema) { s.Default = json.RawMessage(`"d"`) }),
			want:   true,
		},
		"deprecated":     {schema: withRef(func(s *jsonschema.Schema) { s.Deprecated = true }), want: true},
		"readOnly":       {schema: withRef(func(s *jsonschema.Schema) { s.ReadOnly = true }), want: true},
		"writeOnly":      {schema: withRef(func(s *jsonschema.Schema) { s.WriteOnly = true }), want: true},
		"examples":       {schema: withRef(func(s *jsonschema.Schema) { s.Examples = []any{1} }), want: true},
		"$comment":       {schema: withRef(func(s *jsonschema.Schema) { s.Comment = "c" }), want: true},
		"$id":            {schema: withRef(func(s *jsonschema.Schema) { s.ID = "https://x" }), want: true},
		"$schema":        {schema: withRef(func(s *jsonschema.Schema) { s.Schema = "https://s" }), want: true},
		"$anchor":        {schema: withRef(func(s *jsonschema.Schema) { s.Anchor = "a" }), want: true},
		"$dynamicAnchor": {schema: withRef(func(s *jsonschema.Schema) { s.DynamicAnchor = "da" }), want: true},
		"$vocabulary": {
			schema: withRef(func(s *jsonschema.Schema) { s.Vocabulary = map[string]bool{"v": true} }),
			want:   true,
		},
		"extra": {
			schema: withRef(func(s *jsonschema.Schema) { s.Extra = map[string]any{"k": "v"} }),
			want:   true,
		},

		// Constraint keywords: detected via IsEmpty after clearing $ref.
		"type constraint":     {schema: withRef(func(s *jsonschema.Schema) { s.Type = "string" }), want: true},
		"pattern constraint":  {schema: withRef(func(s *jsonschema.Schema) { s.Pattern = "^x" }), want: true},
		"required constraint": {schema: withRef(func(s *jsonschema.Schema) { s.Required = []string{"a"} }), want: true},
		"allOf constraint": {
			schema: withRef(func(s *jsonschema.Schema) { s.AllOf = []*jsonschema.Schema{{}} }),
			want:   true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, schemashape.HasRefSiblings(tt.schema))
		})
	}
}
