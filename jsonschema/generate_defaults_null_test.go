package jsonschema_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// The JSON type names the hook payloads below author.
const (
	typeString = "string"
	typeArray  = "array"
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
// jsonschema tag while the field node's own null decision stays false. A
// node-level predicate would read that decision and drop the default, so this
// row guards the divergence.
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

// defaultsNullExtracted is a named slice that reaches $defs. A named non-struct
// type earns an entry only by implementing a schema hook. The def body carries
// the container null, so a pointer reference to it renders as a bare $ref with
// no null wrapper beside it.
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

// defaultsNullRefSibling is defaultsNullRef with a description on the
// reference. The description moves the $ref into an allOf branch under
// Draft-07, the shape both drafts must answer alike.
type defaultsNullRefSibling struct {
	Tags *defaultsNullExtracted `json:"tags" jsonschema:"description=the tags"`
	Host string                 `json:"host"`
}

// defaultsNullTagged carries a tag default on the field the instance
// leaves nil. A skipped key writes nothing, so the tag default survives.
type defaultsNullTagged struct {
	Ptr  *string `json:"ptr"  jsonschema:"default=fallback"`
	Host string  `json:"host"`
}

// defaultsNullUnion holds the two shapes a hook authors a null into, an enum
// member and a oneOf branch, neither of which the generator emits from
// reflection alone.
type defaultsNullUnion struct {
	Enum  *string `json:"enum"`
	OneOf *string `json:"oneOf"`
	Host  string  `json:"host"`
}

// unionNullExtender authors a null into defaultsNullUnion's enum and oneOf, the
// two encodings a hook can put one in.
func unionNullExtender() jsonschema.GenerateOption {
	return jsonschema.WithTypeSchemaExtenderFor[defaultsNullUnion](
		func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
			ts.Value.Properties["enum"] = &jsonschema.Schema{Enum: []any{nil, "a"}}
			ts.Value.Properties["oneOf"] = &jsonschema.Schema{
				OneOf: []*jsonschema.Schema{{Type: "string"}, {Type: "null"}},
			}

			return nil
		},
	)
}

// defaultsNullUnionDef is a named type whose hook replaces its body with a
// union naming null. The union lands in the shared $defs body rather than on
// the reference, the placement that makes the reference itself say nothing.
type defaultsNullUnionDef string

// JSONSchemaExtend authors the def body's union. It earns defaultsNullUnionDef
// its $defs entry too, which a named non-struct type gets only from a hook.
func (defaultsNullUnionDef) JSONSchemaExtend(
	_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema,
) error {
	ts.Value.Type = ""
	ts.Value.AnyOf = []*jsonschema.Schema{{Type: "string"}, {Type: "null"}}

	return nil
}

// defaultsNullDefUnion reaches defaultsNullUnionDef through a pointer. Under
// WithNullable(false) the reference takes no null wrapper, so the property is a
// bare $ref and only the def body answers.
type defaultsNullDefUnion struct {
	V    *defaultsNullUnionDef `json:"v"`
	Host string                `json:"host"`
}

// defaultsNullCycle is the type whose hook aliases a property schema into its
// own anyOf. Nothing in the payload names null, so the answer is no; the row
// exists because reaching that answer must terminate.
type defaultsNullCycle struct {
	V    *string `json:"v"`
	Host string  `json:"host"`
}

// cycleNullExtender aliases defaultsNullCycle's property schema into its own
// anyOf. A build-time extender's payload keeps its aliases, so this is the
// cycle a decoded payload could never carry.
func cycleNullExtender() jsonschema.GenerateOption {
	return jsonschema.WithTypeSchemaExtenderFor[defaultsNullCycle](
		func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
			self := &jsonschema.Schema{}
			self.AnyOf = []*jsonschema.Schema{{Type: typeString}, self}
			ts.Value.Properties["v"] = self

			return nil
		},
	)
}

// defaultsNullIntersection is the type whose hook authors an allOf holding a
// null-admitting $ref beside a branch that forbids null. An intersection admits
// null only if every branch does, so the reference speaks for no more than
// itself here. It keeps the default nullability, which is what leaves the null
// in the referenced def body.
type defaultsNullIntersection struct {
	V    *defaultsNullExtracted `json:"v"`
	Host string                 `json:"host"`
}

// intersectionNullExtender replaces the reference with the intersection that
// contains it, the shape propRef must not read under 2020-12.
func intersectionNullExtender(ref string) jsonschema.GenerateOption {
	return jsonschema.WithTypeSchemaExtenderFor[defaultsNullIntersection](
		func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
			ts.Value.Properties["v"] = &jsonschema.Schema{
				AllOf: []*jsonschema.Schema{{Ref: ref}, {Type: typeArray}},
			}

			return nil
		},
	)
}

// defaultsNullOtherObj is the object def a hook branch points at. An object
// admits no null, so an intersection holding it admits none either.
type defaultsNullOtherObj struct {
	X int `json:"x"`
}

// defaultsNullWrapPlusHook carries two allOf branches on one property under
// Draft-07: the hook's, appended first, and the generator's $ref wrap, landing
// after it. The wrap's own target admits null and the hook's does not, so the
// intersection admits none. Reading the wrap's branch alone would answer the
// opposite and seed a null the property rejects.
type defaultsNullWrapPlusHook struct {
	Tags  *defaultsNullExtracted `json:"tags"  jsonschema:"description=the tags"`
	Other defaultsNullOtherObj   `json:"other"`
}

// wrapPlusHookExtender appends a $ref-only branch to the tags property in
// place. Mutating the payload rather than replacing it leaves the same schema
// for the Draft-07 wrap to append to.
func wrapPlusHookExtender() jsonschema.GenerateOption {
	return jsonschema.WithTypeSchemaExtenderFor[defaultsNullWrapPlusHook](
		func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
			tags := ts.Value.Properties["tags"]
			tags.AllOf = append(tags.AllOf,
				&jsonschema.Schema{Ref: "#/definitions/defaultsNullOtherObj"})

			return nil
		},
	)
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
		// The name of the $defs entry carrying the seeded properties; empty
		// reads them off the root.
		def string
		// The expected default of every property, as the JSON text the
		// property must carry. An empty string demands no default at all.
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
		"bare ref under draft-07": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullRef](
					t.Context(),
					jsonschema.WithDraft(jsonschema.Draft7),
					jsonschema.WithDefaultsFrom(defaultsNullRef{Host: "localhost"}),
				)
			},
			want: map[string]string{"tags": "null", "host": `"localhost"`},
		},
		"allOf-wrapped ref under draft-07": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullRefSibling](
					t.Context(),
					jsonschema.WithDraft(jsonschema.Draft7),
					jsonschema.WithDefaultsFrom(defaultsNullRefSibling{Host: "localhost"}),
				)
			},
			want: map[string]string{"tags": "null", "host": `"localhost"`},
		},
		"ref with a sibling under 2020-12": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullRefSibling](
					t.Context(),
					jsonschema.WithDefaultsFrom(defaultsNullRefSibling{Host: "localhost"}),
				)
			},
			want: map[string]string{"tags": "null", "host": `"localhost"`},
		},
		"authored enum and oneOf unions": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullUnion](
					t.Context(),
					jsonschema.WithNullable(false),
					unionNullExtender(),
					jsonschema.WithDefaultsFrom(defaultsNullUnion{Host: "localhost"}),
				)
			},
			want: map[string]string{
				"enum": "null", "oneOf": "null", "host": `"localhost"`,
			},
		},
		"tag default under a skipped key": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullTagged](
					t.Context(),
					jsonschema.WithNullable(false),
					jsonschema.WithDefaultsFrom(defaultsNullTagged{Host: "localhost"}),
				)
			},
			want: map[string]string{"ptr": `"fallback"`, "host": `"localhost"`},
		},
		"union in the def body": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullDefUnion](
					t.Context(),
					jsonschema.WithNullable(false),
					jsonschema.WithDefaultsFrom(defaultsNullDefUnion{Host: "localhost"}),
				)
			},
			want: map[string]string{"v": "null", "host": `"localhost"`},
		},
		"payload aliased into its own anyOf": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullCycle](
					t.Context(),
					jsonschema.WithNullable(false),
					cycleNullExtender(),
					jsonschema.WithDefaultsFrom(defaultsNullCycle{Host: "localhost"}),
				)
			},
			want: map[string]string{"v": "", "host": `"localhost"`},
		},
		"hook-authored intersection": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullIntersection](
					t.Context(),
					intersectionNullExtender("#/$defs/defaultsNullExtracted"),
					jsonschema.WithDefaultsFrom(defaultsNullIntersection{Host: "localhost"}),
				)
			},
			want: map[string]string{"v": "", "host": `"localhost"`},
		},
		"draft-07 wrap inside a hook intersection": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullWrapPlusHook](
					t.Context(),
					jsonschema.WithDraft(jsonschema.Draft7),
					wrapPlusHookExtender(),
					jsonschema.WithDefaultsFrom(defaultsNullWrapPlusHook{
						Other: defaultsNullOtherObj{X: 1},
					}),
				)
			},
			want: map[string]string{"tags": "", "other": `{"x":1}`},
		},
		"hook-authored intersection under draft-07": {
			generate: func(t *testing.T) (*jsonschema.Schema, error) {
				t.Helper()

				return jsonschema.GenerateFor[defaultsNullIntersection](
					t.Context(),
					jsonschema.WithDraft(jsonschema.Draft7),
					intersectionNullExtender("#/definitions/defaultsNullExtracted"),
					jsonschema.WithDefaultsFrom(defaultsNullIntersection{Host: "localhost"}),
				)
			},
			want: map[string]string{"v": "", "host": `"localhost"`},
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
