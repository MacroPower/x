package jsonschema_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// mapFS builds an in-memory fs.FS from file name to JSON content.
func mapFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(data)}
	}

	return fsys
}

func TestInline(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		resolver      jsonschema.RefResolver
		files         map[string]string
		schema        string
		baseURI       string
		retrievalBase bool
		want          string
		err           error
	}{
		"pointer ref within document": {
			schema: stringtest.Input(`
				{
					"properties": {
						"a": {"type": "integer"},
						"b": {"$ref": "#/properties/a"}
					}
				}
			`),
			want: stringtest.Input(`
				{
					"properties": {
						"a": {"type": "integer"},
						"b": {"type": "integer"}
					}
				}
			`),
		},
		"defs ref": {
			schema: stringtest.Input(`
				{
					"$defs": {"s": {"type": "string"}},
					"properties": {"a": {"$ref": "#/$defs/s"}}
				}
			`),
			want: stringtest.Input(`
				{
					"$defs": {"s": {"type": "string"}},
					"properties": {"a": {"type": "string"}}
				}
			`),
		},
		"anchor ref": {
			schema: stringtest.Input(`
				{
					"$defs": {"s": {"$anchor": "leaf", "type": "string"}},
					"items": {"$ref": "#leaf"}
				}
			`),
			want: stringtest.Input(`
				{
					"$defs": {"s": {"$anchor": "leaf", "type": "string"}},
					"items": {"$anchor": "leaf", "type": "string"}
				}
			`),
		},
		"ref with siblings under draft 2020-12 joins allOf": {
			schema: stringtest.Input(`
				{
					"$defs": {"s": {"type": "string"}},
					"properties": {"a": {"$ref": "#/$defs/s", "minLength": 3}}
				}
			`),
			want: stringtest.Input(`
				{
					"$defs": {"s": {"type": "string"}},
					"properties": {
						"a": {"minLength": 3, "allOf": [{"type": "string"}]}
					}
				}
			`),
		},
		"ref with siblings under draft 7 drops siblings": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"definitions": {"s": {"type": "string"}},
					"properties": {"a": {"$ref": "#/definitions/s", "minLength": 3}}
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"definitions": {"s": {"type": "string"}},
					"properties": {"a": {"type": "string"}}
				}
			`),
		},
		"draft 7 replacement keeps later refs into sibling definitions resolving": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {
						"destroyer": {"$ref": "#/definitions/b", "definitions": {"inner": {"type": "string"}}},
						"p": {"$ref": "#/definitions/c"}
					},
					"definitions": {
						"b": {"definitions": {"inner": {"type": "number"}}},
						"c": {"$ref": "#/properties/destroyer/definitions/inner"}
					}
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {
						"destroyer": {"definitions": {"inner": {"type": "number"}}},
						"p": {"type": "string"}
					},
					"definitions": {
						"b": {"definitions": {"inner": {"type": "number"}}},
						"c": {"type": "string"}
					}
				}
			`),
		},
		"draft 7 replacement that removes the pointer path keeps later refs resolving": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {
						"destroyer": {"$ref": "#/definitions/b", "definitions": {"inner": {"type": "string"}}},
						"p": {"$ref": "#/properties/destroyer/definitions/inner"}
					},
					"definitions": {"b": {"type": "number"}}
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {
						"destroyer": {"type": "number"},
						"p": {"type": "string"}
					},
					"definitions": {"b": {"type": "number"}}
				}
			`),
		},
		"ref into the sibling defs of a 2020-12 ref node": {
			schema: stringtest.Input(`
				{
					"$defs": {"b": {"type": "object"}},
					"properties": {
						"destroyer": {"$ref": "#/$defs/b", "$defs": {"inner": {"type": "string"}}},
						"p": {"$ref": "#/properties/destroyer/$defs/inner"}
					}
				}
			`),
			want: stringtest.Input(`
				{
					"$defs": {"b": {"type": "object"}},
					"properties": {
						"destroyer": {
							"$defs": {"inner": {"type": "string"}},
							"allOf": [{"type": "object"}]
						},
						"p": {"type": "string"}
					}
				}
			`),
		},
		"ref into a location only splicing would create does not resolve": {
			schema: stringtest.Input(`
				{
					"$defs": {"s": {"type": "string"}},
					"properties": {
						"x": {"$ref": "#/$defs/s", "minLength": 3},
						"y": {"$ref": "#/properties/x/allOf/0"}
					}
				}
			`),
			err: jsonschema.ErrRefResolve,
		},
		"draft 7 root ref keeps the input dialect": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"definitions": {"s": {"type": "string"}},
					"$ref": "#/definitions/s"
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"type": "string"
				}
			`),
		},
		"chained refs": {
			schema: stringtest.Input(`
				{
					"$defs": {
						"a": {"$ref": "#/$defs/b"},
						"b": {"$ref": "#/$defs/c"},
						"c": {"type": "number"}
					},
					"properties": {"p": {"$ref": "#/$defs/a"}}
				}
			`),
			want: stringtest.Input(`
				{
					"$defs": {
						"a": {"type": "number"},
						"b": {"type": "number"},
						"c": {"type": "number"}
					},
					"properties": {"p": {"type": "number"}}
				}
			`),
		},
		"cross-document ref via relative file paths": {
			schema:  `{"$ref": "sub/child.json"}`,
			baseURI: "main.json",
			files: map[string]string{
				"sub/child.json": `{"properties": {"x": {"$ref": "leaf.json"}}}`,
				"sub/leaf.json":  `{"type": "boolean"}`,
			},
			want: `{"properties": {"x": {"type": "boolean"}}}`,
		},
		"back-ref to the root from a fetched document uses the in-memory root": {
			// The fs cannot serve main.json, so the case fails unless the
			// back-ref resolves to the in-memory root instead of re-fetching.
			schema: stringtest.Input(`
				{
					"$defs": {"name": {"type": "string"}},
					"properties": {"child": {"$ref": "other.json"}}
				}
			`),
			baseURI: "main.json",
			files: map[string]string{
				"other.json": `{"$ref": "main.json#/$defs/name"}`,
			},
			want: stringtest.Input(`
				{
					"$defs": {"name": {"type": "string"}},
					"properties": {"child": {"type": "string"}}
				}
			`),
		},
		"self-ref by the root document's own URI needs no resolver": {
			schema: stringtest.Input(`
				{
					"$defs": {"port": {"type": "integer"}},
					"properties": {"a": {"$ref": "main.json#/$defs/port"}}
				}
			`),
			baseURI: "main.json",
			want: stringtest.Input(`
				{
					"$defs": {"port": {"type": "integer"}},
					"properties": {"a": {"type": "integer"}}
				}
			`),
		},
		"ref into a pointer fragment of a fetched document": {
			schema: `{"$ref": "defs.json#/$defs/port"}`,
			files: map[string]string{
				"defs.json": `{"$defs": {"port": {"type": "integer", "minimum": 1}}}`,
			},
			want: `{"type": "integer", "minimum": 1}`,
		},
		"ref into an anchor fragment of a fetched document": {
			schema: `{"$ref": "anchored.json#prt"}`,
			files: map[string]string{
				"anchored.json": `{"$defs": {"p": {"$anchor": "prt", "type": "integer"}}}`,
			},
			want: `{"$anchor": "prt", "type": "integer"}`,
		},
		"mutually referencing defs are a cycle": {
			schema: stringtest.Input(`
				{
					"$defs": {
						"a": {"$ref": "#/$defs/b"},
						"b": {"$ref": "#/$defs/a"}
					}
				}
			`),
			err: jsonschema.ErrRefCycle,
		},
		"recursive schema is a cycle": {
			schema: stringtest.Input(`
				{
					"$defs": {
						"node": {"properties": {"next": {"$ref": "#/$defs/node"}}}
					},
					"$ref": "#/$defs/node"
				}
			`),
			err: jsonschema.ErrRefCycle,
		},
		"cross-document cycle": {
			schema: `{"$ref": "a.json"}`,
			files: map[string]string{
				"a.json": `{"$ref": "b.json"}`,
				"b.json": `{"$ref": "a.json"}`,
			},
			err: jsonschema.ErrRefCycle,
		},
		"dynamicRef has no static expansion": {
			schema: stringtest.Input(`
				{
					"$defs": {"x": {"$dynamicAnchor": "it"}},
					"items": {"$dynamicRef": "#it"}
				}
			`),
			err: jsonschema.ErrRefInline,
		},
		"non-local ref with no resolver": {
			schema: `{"$ref": "http://example.com/x.json"}`,
			err:    jsonschema.ErrRefResolve,
		},
		"unresolvable local pointer": {
			schema: `{"$ref": "#/$defs/missing"}`,
			err:    jsonschema.ErrRefResolve,
		},
		"resolver cannot read the document": {
			schema: `{"$ref": "missing.json"}`,
			files:  map[string]string{},
			err:    jsonschema.ErrRefResolve,
		},
		"resolver returns no schema": {
			schema:   `{"$ref": "http://example.com/x.json"}`,
			resolver: mapResolver{},
			err:      jsonschema.ErrRefResolve,
		},
		"unresolvable fragment of a fetched document": {
			schema: `{"$ref": "defs.json#/$defs/missing"}`,
			files: map[string]string{
				"defs.json": `{"$defs": {"port": {"type": "integer"}}}`,
			},
			err: jsonschema.ErrRefResolve,
		},
		"remote root $id absolutizes refs away from disk by default": {
			schema: stringtest.Input(`
				{
					"$id": "https://example.com/schemas/main.json",
					"properties": {"child": {"$ref": "sub/child.json"}}
				}
			`),
			baseURI: "main.json",
			files: map[string]string{
				"sub/child.json": `{"type": "string"}`,
			},
			err: jsonschema.ErrRefResolve,
		},
		"retrieval base resolves refs from disk despite a remote root $id": {
			schema: stringtest.Input(`
				{
					"$id": "https://example.com/schemas/main.json",
					"properties": {"child": {"$ref": "sub/child.json"}}
				}
			`),
			baseURI:       "main.json",
			retrievalBase: true,
			files: map[string]string{
				"sub/child.json": `{"type": "string"}`,
			},
			want: stringtest.Input(`
				{
					"$id": "https://example.com/schemas/main.json",
					"properties": {"child": {"type": "string"}}
				}
			`),
		},
		"retrieval base keeps nested $id verbatim and resolves fetched refs against the fetch URI": {
			schema:        `{"$ref": "sub/child.json"}`,
			baseURI:       "main.json",
			retrievalBase: true,
			files: map[string]string{
				"sub/child.json": stringtest.Input(`
					{
						"$id": "https://example.com/child.json",
						"properties": {"x": {"$ref": "leaf.json"}}
					}
				`),
				"sub/leaf.json": `{"type": "boolean"}`,
			},
			want: stringtest.Input(`
				{
					"$id": "https://example.com/child.json",
					"properties": {"x": {"type": "boolean"}}
				}
			`),
		},
		"retrieval base keeps anchors resolving within their document": {
			schema: stringtest.Input(`
				{
					"$id": "https://example.com/x.json",
					"$defs": {"s": {"$anchor": "leaf", "type": "string"}},
					"items": {"$ref": "#leaf"}
				}
			`),
			retrievalBase: true,
			want: stringtest.Input(`
				{
					"$id": "https://example.com/x.json",
					"$defs": {"s": {"$anchor": "leaf", "type": "string"}},
					"items": {"$anchor": "leaf", "type": "string"}
				}
			`),
		},
		"ref to a nested $id URI resolves from the registry by default": {
			schema: stringtest.Input(`
				{
					"$defs": {"s": {"$id": "https://example.com/y.json", "type": "string"}},
					"properties": {"a": {"$ref": "https://example.com/y.json"}}
				}
			`),
			want: stringtest.Input(`
				{
					"$defs": {"s": {"$id": "https://example.com/y.json", "type": "string"}},
					"properties": {"a": {"$id": "https://example.com/y.json", "type": "string"}}
				}
			`),
		},
		"retrieval base does not register $id as a resolution target": {
			schema: stringtest.Input(`
				{
					"$defs": {"s": {"$id": "https://example.com/y.json", "type": "string"}},
					"properties": {"a": {"$ref": "https://example.com/y.json"}}
				}
			`),
			retrievalBase: true,
			err:           jsonschema.ErrRefResolve,
		},
		"retrieval base makes the draft-7 fragment-only $id anchor form inert": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"definitions": {"s": {"$id": "#frag", "type": "string"}},
					"properties": {"a": {"$ref": "#frag"}}
				}
			`),
			retrievalBase: true,
			err:           jsonschema.ErrRefResolve,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var schema jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))

			var opts []jsonschema.InlineOption

			if tc.files != nil {
				opts = append(opts, jsonschema.WithRefResolver(jsonschema.NewFileResolver(mapFS(tc.files))))
			}

			if tc.resolver != nil {
				opts = append(opts, jsonschema.WithRefResolver(tc.resolver))
			}

			if tc.baseURI != "" {
				opts = append(opts, jsonschema.WithBaseURI(tc.baseURI))
			}

			if tc.retrievalBase {
				opts = append(opts, jsonschema.WithRetrievalBase(true))
			}

			got, err := jsonschema.Inline(t.Context(), &schema, opts...)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)

			data, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}

// refFallbackCall records one consultation of a [jsonschema.WithRefFallback]
// policy: the URI of the containing document, the JSON Pointer path of the
// referencing schema within that document, the reference value, and the
// sentinel the error wraps.
type refFallbackCall struct {
	doc  string
	path string
	ref  string
	err  error
}

func TestInlineRefFallback(t *testing.T) {
	t.Parallel()

	drop := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		return jsonschema.DropRef()
	})
	decline := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		return jsonschema.PropagateRef()
	})
	replaceWith := func(s *jsonschema.Schema) jsonschema.RefFallback {
		return jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
			return jsonschema.SubstituteRef(s)
		})
	}

	tests := map[string]struct {
		files         map[string]string
		schema        string
		baseURI       string
		retrievalBase bool
		fallback      jsonschema.RefFallback
		wantCalls     []refFallbackCall
		want          string
		err           error
	}{
		"no fallback keeps strict resolve errors": {
			schema: `{"$ref": "#/$defs/missing"}`,
			err:    jsonschema.ErrRefResolve,
		},
		"drop on a dangling local pointer keeps siblings and clears the ref": {
			schema: stringtest.Input(`
				{
					"properties": {"a": {"$ref": "#/$defs/missing", "minLength": 3}}
				}
			`),
			fallback: drop,
			wantCalls: []refFallbackCall{
				{path: "/properties/a", ref: "#/$defs/missing", err: jsonschema.ErrRefResolve},
			},
			want: `{"properties": {"a": {"minLength": 3}}}`,
		},
		"substitute joins the node's allOf under draft 2020-12": {
			schema: stringtest.Input(`
				{
					"properties": {"a": {"$ref": "#/$defs/missing", "minLength": 3}}
				}
			`),
			fallback: replaceWith(&jsonschema.Schema{Type: "string"}),
			wantCalls: []refFallbackCall{
				{path: "/properties/a", ref: "#/$defs/missing", err: jsonschema.ErrRefResolve},
			},
			want: stringtest.Input(`
				{
					"properties": {
						"a": {"minLength": 3, "allOf": [{"type": "string"}]}
					}
				}
			`),
		},
		"substitute replaces the node wholesale under draft 7": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {"a": {"$ref": "#/definitions/missing", "minLength": 3}}
				}
			`),
			fallback: replaceWith(&jsonschema.Schema{Type: "string"}),
			wantCalls: []refFallbackCall{
				{path: "/properties/a", ref: "#/definitions/missing", err: jsonschema.ErrRefResolve},
			},
			want: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {"a": {"type": "string"}}
				}
			`),
		},
		"substitute replaces a bare ref node wholesale": {
			schema:   `{"properties": {"a": {"$ref": "#/$defs/missing"}}}`,
			fallback: replaceWith(&jsonschema.Schema{Type: "integer"}),
			wantCalls: []refFallbackCall{
				{path: "/properties/a", ref: "#/$defs/missing", err: jsonschema.ErrRefResolve},
			},
			want: `{"properties": {"a": {"type": "integer"}}}`,
		},
		"declining propagates the original error": {
			schema:   `{"properties": {"a": {"$ref": "#/$defs/missing"}}}`,
			fallback: decline,
			wantCalls: []refFallbackCall{
				{path: "/properties/a", ref: "#/$defs/missing", err: jsonschema.ErrRefResolve},
			},
			err: jsonschema.ErrRefResolve,
		},
		"cycle consults the innermost ref once and drop breaks it": {
			schema: stringtest.Input(`
				{
					"$defs": {
						"a": {"$ref": "#/$defs/b"},
						"b": {"$ref": "#/$defs/a"}
					}
				}
			`),
			fallback: drop,
			wantCalls: []refFallbackCall{
				{path: "/$defs/a", ref: "#/$defs/b", err: jsonschema.ErrRefCycle},
			},
			want: `{"$defs": {"a": true, "b": true}}`,
		},
		"nested failure consults the failing ref only and a decline propagates": {
			schema: stringtest.Input(`
				{
					"$defs": {"mid": {"$ref": "#/missing"}},
					"properties": {"a": {"$ref": "#/$defs/mid"}}
				}
			`),
			fallback: decline,
			wantCalls: []refFallbackCall{
				{path: "/$defs/mid", ref: "#/missing", err: jsonschema.ErrRefResolve},
			},
			err: jsonschema.ErrRefResolve,
		},
		"dynamicRef consults with ErrRefInline and drop keeps siblings": {
			schema: stringtest.Input(`
				{
					"$defs": {"x": {"$dynamicAnchor": "it"}},
					"items": {"$dynamicRef": "#it", "description": "d"}
				}
			`),
			fallback: drop,
			wantCalls: []refFallbackCall{
				{path: "/items", ref: "#it", err: jsonschema.ErrRefInline},
			},
			want: stringtest.Input(`
				{
					"$defs": {"x": {"$dynamicAnchor": "it"}},
					"items": {"description": "d"}
				}
			`),
		},
		"dynamicRef substitute replaces a bare node wholesale": {
			schema:   `{"items": {"$dynamicRef": "#it"}}`,
			fallback: replaceWith(&jsonschema.Schema{Type: "string"}),
			wantCalls: []refFallbackCall{
				{path: "/items", ref: "#it", err: jsonschema.ErrRefInline},
			},
			want: `{"items": {"type": "string"}}`,
		},
		"path names an additionalProperties position": {
			schema:   `{"additionalProperties": {"$ref": "#/$defs/missing"}}`,
			fallback: drop,
			wantCalls: []refFallbackCall{
				{path: "/additionalProperties", ref: "#/$defs/missing", err: jsonschema.ErrRefResolve},
			},
			want: `{"additionalProperties": true}`,
		},
		"path descends nested defs": {
			schema: stringtest.Input(`
				{
					"$defs": {
						"outer": {"properties": {"b": {"$ref": "#/nope", "title": "t"}}}
					}
				}
			`),
			fallback: drop,
			wantCalls: []refFallbackCall{
				{path: "/$defs/outer/properties/b", ref: "#/nope", err: jsonschema.ErrRefResolve},
			},
			want: stringtest.Input(`
				{
					"$defs": {
						"outer": {"properties": {"b": {"title": "t"}}}
					}
				}
			`),
		},
		"document is the root's id when it declares one": {
			schema: stringtest.Input(`
				{
					"$id": "https://example.com/root.json",
					"properties": {"a": {"$ref": "#/$defs/missing", "minLength": 3}}
				}
			`),
			fallback: drop,
			wantCalls: []refFallbackCall{
				{
					doc:  "https://example.com/root.json",
					path: "/properties/a",
					ref:  "#/$defs/missing",
					err:  jsonschema.ErrRefResolve,
				},
			},
			want: stringtest.Input(`
				{
					"$id": "https://example.com/root.json",
					"properties": {"a": {"minLength": 3}}
				}
			`),
		},
		"fallback in a fetched document gets that document's local path": {
			schema:  `{"$ref": "child.json"}`,
			baseURI: "main.json",
			files: map[string]string{
				"child.json": `{"properties": {"x": {"$ref": "#/missing", "minLength": 1}}}`,
			},
			fallback: drop,
			wantCalls: []refFallbackCall{
				{doc: "file:///child.json", path: "/properties/x", ref: "#/missing", err: jsonschema.ErrRefResolve},
			},
			want: `{"properties": {"x": {"minLength": 1}}}`,
		},
		"substitute refs resolve in the document containing the failing ref": {
			schema: stringtest.Input(`
				{
					"$defs": {"s": {"type": "string"}},
					"properties": {"a": {"$ref": "#/missing"}}
				}
			`),
			fallback: replaceWith(&jsonschema.Schema{Ref: "#/$defs/s"}),
			wantCalls: []refFallbackCall{
				{path: "/properties/a", ref: "#/missing", err: jsonschema.ErrRefResolve},
			},
			want: stringtest.Input(`
				{
					"$defs": {"s": {"type": "string"}},
					"properties": {"a": {"type": "string"}}
				}
			`),
		},
		"cycle introduced by the substitute is an ordinary cycle error": {
			schema: `{"properties": {"a": {"$ref": "#/missing"}}}`,
			fallback: jsonschema.RefFallbackFunc(func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
				if errors.Is(f.Err, jsonschema.ErrRefCycle) {
					return jsonschema.PropagateRef()
				}

				return jsonschema.SubstituteRef(&jsonschema.Schema{Ref: "#/properties/a"})
			}),
			wantCalls: []refFallbackCall{
				{path: "/properties/a", ref: "#/missing", err: jsonschema.ErrRefResolve},
				{path: "/properties/a", ref: "#/missing", err: jsonschema.ErrRefResolve},
				{path: "/properties/a", ref: "#/properties/a", err: jsonschema.ErrRefCycle},
			},
			err: jsonschema.ErrRefCycle,
		},
		"combines with retrieval base": {
			schema: stringtest.Input(`
				{
					"$id": "https://example.com/main.json",
					"properties": {
						"bad": {"$ref": "missing.json", "description": "d"},
						"child": {"$ref": "child.json"}
					}
				}
			`),
			baseURI:       "main.json",
			retrievalBase: true,
			files: map[string]string{
				"child.json": `{"type": "string"}`,
			},
			fallback: drop,
			wantCalls: []refFallbackCall{
				{doc: "file:///main.json", path: "/properties/bad", ref: "missing.json", err: jsonschema.ErrRefResolve},
			},
			want: stringtest.Input(`
				{
					"$id": "https://example.com/main.json",
					"properties": {
						"bad": {"description": "d"},
						"child": {"type": "string"}
					}
				}
			`),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var schema jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))

			var opts []jsonschema.InlineOption

			if tc.files != nil {
				opts = append(opts, jsonschema.WithRefResolver(jsonschema.NewFileResolver(mapFS(tc.files))))
			}

			if tc.baseURI != "" {
				opts = append(opts, jsonschema.WithBaseURI(tc.baseURI))
			}

			if tc.retrievalBase {
				opts = append(opts, jsonschema.WithRetrievalBase(true))
			}

			var calls []refFallbackCall

			if tc.fallback != nil {
				opts = append(opts, jsonschema.WithRefFallback(jsonschema.RefFallbackFunc(
					func(ctx context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
						calls = append(calls, refFallbackCall{doc: f.Document, path: f.Path, ref: f.Ref, err: f.Err})

						return tc.fallback.ResolveRefFailure(ctx, f)
					})))
			}

			got, err := jsonschema.Inline(t.Context(), &schema, opts...)

			require.Len(t, calls, len(tc.wantCalls))

			for i, want := range tc.wantCalls {
				assert.Equal(t, want.doc, calls[i].doc, "call %d document", i)
				assert.Equal(t, want.path, calls[i].path, "call %d path", i)
				assert.Equal(t, want.ref, calls[i].ref, "call %d ref", i)
				require.ErrorIs(t, calls[i].err, want.err, "call %d error", i)
			}

			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)

			data, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}

func TestInlineNil(t *testing.T) {
	t.Parallel()

	got, err := jsonschema.Inline(t.Context(), nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestInlineDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	remote := &jsonschema.Schema{
		Defs: map[string]*jsonschema.Schema{
			"port": {Type: "integer", Minimum: jsonschema.Ptr(float64(1))},
		},
	}
	schema := &jsonschema.Schema{
		Defs: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
		},
		Properties: map[string]*jsonschema.Schema{
			"name": {Ref: "#/$defs/name"},
			"port": {Ref: "http://example.com/defs.json#/$defs/port"},
		},
	}

	schemaBefore, err := json.Marshal(schema)
	require.NoError(t, err)

	remoteBefore, err := json.Marshal(remote)
	require.NoError(t, err)

	got, err := jsonschema.Inline(t.Context(), schema, jsonschema.WithRefResolver(mapResolver{
		"http://example.com/defs.json": remote,
	}))
	require.NoError(t, err)

	schemaAfter, err := json.Marshal(schema)
	require.NoError(t, err)

	remoteAfter, err := json.Marshal(remote)
	require.NoError(t, err)

	assert.JSONEq(t, string(schemaBefore), string(schemaAfter), "input schema must not be mutated")
	assert.JSONEq(t, string(remoteBefore), string(remoteAfter), "resolver schema must not be mutated")

	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotEqual(t, string(schemaBefore), string(gotJSON), "the result is a distinct, inlined copy")
}

// TestInlineValidatesIdentically pins the behavior contract: the inlined
// schema, compiled without any resolver, accepts and rejects the same
// instances as the original compiled with one.
func TestInlineValidatesIdentically(t *testing.T) {
	t.Parallel()

	fsys := mapFS(map[string]string{
		"defs.json": `{"$defs": {"port": {"type": "integer", "minimum": 1, "maximum": 65535}}}`,
	})

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(stringtest.Input(`
		{
			"$defs": {
				"name": {"type": "string", "minLength": 2},
				"tag": {"$anchor": "tagAnchor", "type": "string", "pattern": "^[a-z]+$"}
			},
			"type": "object",
			"properties": {
				"name": {"$ref": "#/$defs/name"},
				"port": {"$ref": "defs.json#/$defs/port"},
				"tag": {"$ref": "#tagAnchor", "description": "release tag"}
			},
			"required": ["name"],
			"additionalProperties": false
		}
	`)), &schema))

	resolver := jsonschema.NewFileResolver(fsys)

	inlined, err := jsonschema.Inline(t.Context(), &schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	original, err := jsonschema.Compile(t.Context(), &schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	// No resolver: the inlined schema must be self-contained.
	standalone, err := jsonschema.Compile(t.Context(), inlined)
	require.NoError(t, err)

	instances := map[string]any{
		"all constraints satisfied": map[string]any{"name": "ab", "port": 80.0, "tag": "abc"},
		"name too short":            map[string]any{"name": "a"},
		"port out of range":         map[string]any{"name": "ab", "port": 70000.0},
		"tag pattern violated":      map[string]any{"name": "ab", "tag": "ABC"},
		"required name missing":     map[string]any{"port": 80.0},
		"unknown property":          map[string]any{"name": "ab", "extra": true},
	}

	for name, instance := range instances {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			origErr := original.Validate(t.Context(), instance)
			inlinedErr := standalone.Validate(t.Context(), instance)

			if origErr == nil {
				assert.NoError(t, inlinedErr)
			} else {
				assert.Error(t, inlinedErr)
			}
		})
	}
}

// TestInlineValidatesIdenticallyWithRefSiblingDefinitions pins the contract
// on a draft-7 document where a ref node carries sibling definitions that a
// later ref targets: refs resolve against the original structure, never
// against an already-expanded node, so the inlined schema accepts and
// rejects the same instances as the compiled original.
func TestInlineValidatesIdenticallyWithRefSiblingDefinitions(t *testing.T) {
	t.Parallel()

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(stringtest.Input(`
		{
			"$schema": "http://json-schema.org/draft-07/schema#",
			"properties": {
				"destroyer": {"$ref": "#/definitions/b", "definitions": {"inner": {"type": "string"}}},
				"p": {"$ref": "#/definitions/c"}
			},
			"definitions": {
				"b": {"definitions": {"inner": {"type": "number"}}},
				"c": {"$ref": "#/properties/destroyer/definitions/inner"}
			}
		}
	`)), &schema))

	inlined, err := jsonschema.Inline(t.Context(), &schema)
	require.NoError(t, err)

	original, err := jsonschema.Compile(t.Context(), &schema)
	require.NoError(t, err)

	standalone, err := jsonschema.Compile(t.Context(), inlined)
	require.NoError(t, err)

	valid := map[string]any{"p": "hello"}
	require.NoError(t, original.Validate(t.Context(), valid), "the original accepts a string p")
	assert.NoError(t, standalone.Validate(t.Context(), valid), "the inlined schema accepts a string p")

	invalid := map[string]any{"p": 5.0}
	require.Error(t, original.Validate(t.Context(), invalid), "the original rejects a numeric p")
	assert.Error(t, standalone.Validate(t.Context(), invalid), "the inlined schema rejects a numeric p")
}

func TestFileResolver(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"schemas/a.json": `{"type": "string"}`,
		"broken.json":    `{not json`,
	}

	tests := map[string]struct {
		uri  string
		want string
	}{
		"plain relative path": {
			uri:  "schemas/a.json",
			want: `{"type": "string"}`,
		},
		"leading slash addresses the fs root": {
			uri:  "/schemas/a.json",
			want: `{"type": "string"}`,
		},
		"file scheme stripped": {
			uri:  "file:///schemas/a.json",
			want: `{"type": "string"}`,
		},
		"missing file": {
			uri: "schemas/missing.json",
		},
		"malformed document": {
			uri: "broken.json",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resolver := jsonschema.NewFileResolver(mapFS(files))

			got, err := resolver.ResolveRef(t.Context(), tc.uri)
			if tc.want == "" {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)

			data, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}

// TestFileResolverWithValidation pins the documented dual use: the same
// resolver Inline pairs with WithBaseURI also serves file-path and
// relative refs during validation via WithRefResolver, while a ref that
// absolutizes to another scheme is not a valid fs path and surfaces as a
// validation failure.
func TestFileResolverWithValidation(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.NewFileResolver(mapFS(map[string]string{
		"child.json": `{"type": "integer"}`,
	}))

	schema := func(ref string) *jsonschema.Schema {
		return &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"count": {Ref: ref},
			},
		}
	}

	for name, ref := range map[string]string{
		"relative ref":      "child.json",
		"file absolute ref": "file:///child.json",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.Compile(t.Context(), schema(ref), jsonschema.WithRefResolver(resolver))
			require.NoError(t, err)

			require.NoError(t, v.Validate(t.Context(), map[string]any{"count": 1.0}))
			require.Error(t, v.Validate(t.Context(), map[string]any{"count": "nope"}))
		})
	}

	t.Run("http ref misses", func(t *testing.T) {
		t.Parallel()

		v, err := jsonschema.Compile(t.Context(), schema("https://example.com/child.json"),
			jsonschema.WithRefResolver(resolver),
		)
		require.NoError(t, err)

		err = v.Validate(t.Context(), map[string]any{"count": 1.0})
		require.Error(t, err)
		require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	})
}

// TestStripPrefix covers serving refs that absolutize against a published
// remote base from disk: the middleware strips the prefix before delegating,
// so an https URI becomes an fs path, while URIs without the prefix are
// delegated unchanged.
func TestStripPrefix(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.StripPrefix("https://example.com/schemas/",
		jsonschema.NewFileResolver(mapFS(map[string]string{
			"child.json": `{"type": "integer"}`,
		})))

	t.Run("prefixed URI serves from the fs", func(t *testing.T) {
		t.Parallel()

		s, err := resolver.ResolveRef(t.Context(), "https://example.com/schemas/child.json")
		require.NoError(t, err)
		assert.Equal(t, "integer", s.Type)
	})

	t.Run("unprefixed URI is delegated unchanged", func(t *testing.T) {
		t.Parallel()

		s, err := resolver.ResolveRef(t.Context(), "file:///child.json")
		require.NoError(t, err)
		assert.Equal(t, "integer", s.Type)
	})

	t.Run("other remote bases still miss", func(t *testing.T) {
		t.Parallel()

		_, err := resolver.ResolveRef(t.Context(), "https://other.example/child.json")
		require.Error(t, err)
	})
}

// TestValidateWithBaseURI pins WithBaseURI as a shared option during
// validation: the root document's relative refs absolutize against the base,
// a relative ref inside a fetched document resolves against that document's
// URI, and a ref absolutizing back to the root resolves to the in-memory
// document instead of being fetched (the fs serves no main.json, so the case
// fails without that registration).
func TestValidateWithBaseURI(t *testing.T) {
	t.Parallel()

	resolver := jsonschema.NewFileResolver(mapFS(map[string]string{
		"sub/child.json": `{"properties": {"x": {"$ref": "leaf.json"}}}`,
		"sub/leaf.json":  `{"type": "boolean"}`,
	}))

	schema := &jsonschema.Schema{
		Type: "object",
		Defs: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
		},
		Properties: map[string]*jsonschema.Schema{
			"child": {Ref: "sub/child.json"},
			"self":  {Ref: "main.json#/$defs/name"},
		},
	}

	v, err := jsonschema.Compile(t.Context(), schema,
		jsonschema.WithRefResolver(resolver),
		jsonschema.WithBaseURI("main.json"),
	)
	require.NoError(t, err)

	valid := map[string]any{"child": map[string]any{"x": true}, "self": "ada"}
	require.NoError(t, v.Validate(t.Context(), valid))

	badChild := map[string]any{"child": map[string]any{"x": "nope"}, "self": "ada"}
	require.Error(t, v.Validate(t.Context(), badChild),
		"the fetched document's leaf.json ref must constrain x")

	badSelf := map[string]any{"child": map[string]any{"x": true}, "self": 1.0}
	require.Error(t, v.Validate(t.Context(), badSelf),
		"the back-ref into the root document must constrain self")
}

// TestInlineDeepCopyIndependence verifies the deep-copy contract of
// [jsonschema.Inline] from the copy's side: mutating the returned schema must
// never reach back into the input. Because the copy round-trips through JSON,
// every JSON-serializable field -- including maps, slices, and pointers --
// comes back as a fresh value. The probes are ref-free, so Inline is a pure
// deep copy here.
func TestInlineDeepCopyIndependence(t *testing.T) {
	t.Parallel()

	// Build a schema whose Const points at a fresh value, so each case owns its
	// pointer and mutating the copy's *Const can't alias another case's value.
	constSchema := func() *jsonschema.Schema {
		var v any = "a"

		return &jsonschema.Schema{Const: &v}
	}

	tests := map[string]struct {
		schema *jsonschema.Schema
		mutate func(inlined *jsonschema.Schema)
		check  func(t *testing.T, original *jsonschema.Schema)
	}{
		"extra nested map": {
			schema: &jsonschema.Schema{Extra: map[string]any{"x-custom": map[string]any{"nested": "value"}}},
			mutate: func(inlined *jsonschema.Schema) {
				if nested, ok := inlined.Extra["x-custom"].(map[string]any); ok {
					nested["nested"] = "modified"
				}
			},
			check: func(t *testing.T, original *jsonschema.Schema) {
				t.Helper()

				nested, ok := original.Extra["x-custom"].(map[string]any)
				require.True(t, ok, "x-custom should round-trip as a map[string]any")
				assert.Equal(t, "value", nested["nested"], "nested map inside Extra must be independent of the copy")
			},
		},
		"extra top-level value": {
			schema: &jsonschema.Schema{Extra: map[string]any{"x-custom": "value"}},
			mutate: func(inlined *jsonschema.Schema) { inlined.Extra["x-custom"] = "modified" },
			check: func(t *testing.T, original *jsonschema.Schema) {
				t.Helper()

				assert.Equal(t, "value", original.Extra["x-custom"],
					"Extra map must not share backing storage with the copy")
			},
		},
		"enum slice element": {
			schema: &jsonschema.Schema{Enum: []any{"a", "b"}},
			mutate: func(inlined *jsonschema.Schema) { inlined.Enum[0] = "modified" },
			check: func(t *testing.T, original *jsonschema.Schema) {
				t.Helper()

				assert.Equal(t, "a", original.Enum[0], "Enum slice must not share backing storage with the copy")
			},
		},
		"examples slice element": {
			schema: &jsonschema.Schema{Examples: []any{"a", "b"}},
			mutate: func(inlined *jsonschema.Schema) { inlined.Examples[0] = "modified" },
			check: func(t *testing.T, original *jsonschema.Schema) {
				t.Helper()

				assert.Equal(t, "a", original.Examples[0],
					"Examples slice must not share backing storage with the copy")
			},
		},
		"const pointer": {
			schema: constSchema(),
			mutate: func(inlined *jsonschema.Schema) { *inlined.Const = "modified" },
			check: func(t *testing.T, original *jsonschema.Schema) {
				t.Helper()

				require.NotNil(t, original.Const)
				assert.Equal(t, "a", *original.Const, "Const pointer must address a distinct value from the copy")
			},
		},
		"default raw message byte": {
			schema: &jsonschema.Schema{Default: json.RawMessage(`"a"`)},
			mutate: func(inlined *jsonschema.Schema) { inlined.Default[1] = 'X' },
			check: func(t *testing.T, original *jsonschema.Schema) {
				t.Helper()

				assert.Equal(t, `"a"`, string(original.Default),
					"Default bytes must not share backing storage with the copy")
			},
		},
		"nested sub-schema": {
			schema: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"a": {Type: "string"}}},
			mutate: func(inlined *jsonschema.Schema) { inlined.Properties["a"].Type = "integer" },
			check: func(t *testing.T, original *jsonschema.Schema) {
				t.Helper()

				assert.Equal(t, "string", original.Properties["a"].Type,
					"sub-schemas must not be shared with the copy")
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inlined, err := jsonschema.Inline(t.Context(), tc.schema)
			require.NoError(t, err)
			require.NotSame(t, tc.schema, inlined, "Inline must return a distinct *Schema")

			tc.mutate(inlined)
			tc.check(t, tc.schema)
		})
	}
}
