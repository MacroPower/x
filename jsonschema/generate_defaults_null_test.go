package jsonschema_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// defaultsNullRec is the self-referential type a NullForbidden stance covers.
// The pointer field carries no omitempty, so the zero instance marshals it as
// JSON null against a reference the stance refuses. Name is the sibling that
// pins the skip as per-key rather than per-instance.
type defaultsNullRec struct {
	Next *defaultsNullRec `json:"next"`
	Name string           `json:"name"`
}

// defaultsNullContainers holds one nilable field per container kind
// [jsonschema.WithNullable] governs, beside a scalar sibling. None carries
// omitempty, so the zero value of each marshals to JSON null.
type defaultsNullContainers struct {
	Tags []string          `json:"tags"`
	Meta map[string]string `json:"meta"`
	Ptr  *string           `json:"ptr"`
	Host string            `json:"host"`
}

// defaultsNullTyped renders its pointer field as {"type":"null"} through the
// jsonschema tag while the field node's own null decision stays false. It
// guards the divergence the rendered-property predicate answers and a
// node-level one would not.
type defaultsNullTyped struct {
	Void *string `json:"void" jsonschema:"type=null"`
	Host string  `json:"host"`
}

// defaultsNullAny renders its interface field as an empty schema, which admits
// every instance including null.
type defaultsNullAny struct {
	Any  any    `json:"any"`
	Host string `json:"host"`
}

// defaultsNullExtracted is a named slice reaching $defs, which named non-struct
// types do only by implementing a schema hook. Its def body carries the
// container null, so a pointer reference to it renders as a bare $ref with no
// null wrapper beside it.
type defaultsNullExtracted []string

// JSONSchemaExtend is the hook that earns defaultsNullExtracted its $defs
// entry. The description it writes is incidental.
func (defaultsNullExtracted) JSONSchemaExtend(
	_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema,
) error {
	ts.Value.Description = "tags"

	return nil
}

// defaultsNullRef reaches defaultsNullExtracted through a pointer, the
// occurrence whose null the def body already admits.
type defaultsNullRef struct {
	Tags *defaultsNullExtracted `json:"tags"`
	Host string                 `json:"host"`
}

// TestWithDefaultsFromNullDefault pins which properties a null-marshaling key
// seeds. A nil field with neither omitempty nor omitzero marshals to JSON
// null, and [jsonschema.WithDefaultsFrom] writes that null onto the rendered
// property only where the property admits one. Every other key of the same
// instance still seeds its property, so the skip is per key.
func TestWithDefaultsFromNullDefault(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func(t *testing.T) (*jsonschema.Schema, error)
		// Def names the $defs entry carrying the seeded properties; empty
		// reads them off the root.
		def string
		// Want holds the expected default of every property, as the JSON text
		// the property must carry. An empty string demands no default at all.
		want map[string]string
	}{
		"forbidden null on a recursive reference": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullRec](
					t.Context(),
					forbidNullStance[defaultsNullRec](),
					jsonschema.WithDefaultsFrom(defaultsNullRec{Name: "root"}),
				)
			},
			def:  "defaultsNullRec",
			want: map[string]string{"next": "", "name": `"root"`},
		},
		"non-nullable containers": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullContainers](
					t.Context(),
					jsonschema.WithNullable(false),
					jsonschema.WithDefaultsFrom(defaultsNullContainers{Host: "localhost"}),
				)
			},
			want: map[string]string{
				"tags": "", "meta": "", "ptr": "", "host": `"localhost"`,
			},
		},
		"nullable containers": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullContainers](
					t.Context(),
					jsonschema.WithDefaultsFrom(defaultsNullContainers{Host: "localhost"}),
				)
			},
			want: map[string]string{
				"tags": "null", "meta": "null", "ptr": "null", "host": `"localhost"`,
			},
		},
		"tag-typed null": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullTyped](
					t.Context(),
					jsonschema.WithNullable(false),
					jsonschema.WithDefaultsFrom(defaultsNullTyped{Host: "localhost"}),
				)
			},
			want: map[string]string{"void": "null", "host": `"localhost"`},
		},
		"bare ref to a null-admitting def": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullRef](
					t.Context(),
					jsonschema.WithDefaultsFrom(defaultsNullRef{Host: "localhost"}),
				)
			},
			want: map[string]string{"tags": "null", "host": `"localhost"`},
		},
		"empty schema": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullAny](
					t.Context(),
					jsonschema.WithNullable(false),
					jsonschema.WithDefaultsFrom(defaultsNullAny{Host: "localhost"}),
				)
			},
			want: map[string]string{"any": "null", "host": `"localhost"`},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := test.generate(t)
			require.NoError(t, err)
			require.NotNil(t, schema)

			target := schema
			if test.def != "" {
				require.Contains(t, schema.Defs, test.def)

				target = schema.Defs[test.def]
			}

			require.Len(t, target.Properties, len(test.want))

			for key, want := range test.want {
				prop := target.Properties[key]
				require.NotNil(t, prop, "property %q", key)

				if want == "" {
					assert.Empty(t, prop.Default,
						"property %q admits no null and must take no default", key)

					continue
				}

				assert.JSONEq(t, want, string(prop.Default), "property %q", key)
			}
		})
	}
}
