package schemaclone_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

// wrapper is a typed container with an exported schema field; json.Marshal
// serializes the field, so a cycle through it must trip the acyclic check.
type wrapper struct {
	S *jsonschema.Schema `json:"s"`
}

// hidden holds a schema only in an unexported field; json.Marshal skips it,
// so a back-edge through it is not a cycle the round-trip can hit.
type hidden struct {
	s *jsonschema.Schema //nolint:unused // Held only to form a back-edge json.Marshal never serializes.
}

func TestCloneCyclicGraphTypedContainers(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		build func() *jsonschema.Schema
		err   error
	}{
		"cycle through typed schema slice in Extra": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": []*jsonschema.Schema{s}}

				return s
			},
			err: schemaclone.ErrCyclic,
		},
		"cycle through typed schema map in Extra": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": map[string]*jsonschema.Schema{"y": s}}

				return s
			},
			err: schemaclone.ErrCyclic,
		},
		"two-node cycle split across typed slices": {
			build: func() *jsonschema.Schema {
				a := &jsonschema.Schema{Type: "object"}
				b := &jsonschema.Schema{Type: "array"}
				a.Extra = map[string]any{"x": []*jsonschema.Schema{b}}
				b.Extra = map[string]any{"y": []*jsonschema.Schema{a}}

				return a
			},
			err: schemaclone.ErrCyclic,
		},
		"cycle through schema value element in typed slice": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": []jsonschema.Schema{{Items: s}}}

				return s
			},
			err: schemaclone.ErrCyclic,
		},
		"cycle through exported struct field in Examples": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Examples = []any{wrapper{S: s}}

				return s
			},
			err: schemaclone.ErrCyclic,
		},
		"cycle through typed slice nested under any containers": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Enum = []any{map[string]any{"deep": []*jsonschema.Schema{s}}}

				return s
			},
			err: schemaclone.ErrCyclic,
		},
		"acyclic typed schema slice in Extra is not a cycle": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": []*jsonschema.Schema{{Type: "string"}}}

				return s
			},
		},
		"back-edge through unexported struct field is not a cycle": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": hidden{s: s}}

				return s
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cp, err := schemaclone.Clone(tc.build(), children)

			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				assert.Nil(t, cp)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, cp)
		})
	}
}
