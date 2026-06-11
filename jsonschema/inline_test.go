package jsonschema_test

import (
	"encoding/json"
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
		resolver jsonschema.RefResolver
		files    map[string]string
		schema   string
		baseURI  string
		want     string
		err      error
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
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var schema jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))

			var opts []jsonschema.InlineOption

			if tc.files != nil {
				opts = append(opts, jsonschema.WithInlineResolver(jsonschema.FileResolver(mapFS(tc.files))))
			}

			if tc.resolver != nil {
				opts = append(opts, jsonschema.WithInlineResolver(tc.resolver))
			}

			if tc.baseURI != "" {
				opts = append(opts, jsonschema.WithInlineBaseURI(tc.baseURI))
			}

			got, err := jsonschema.Inline(&schema, opts...)
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

	got, err := jsonschema.Inline(nil)
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

	got, err := jsonschema.Inline(schema, jsonschema.WithInlineResolver(mapResolver{
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

	resolver := jsonschema.FileResolver(fsys)

	inlined, err := jsonschema.Inline(&schema, jsonschema.WithInlineResolver(resolver))
	require.NoError(t, err)

	original, err := jsonschema.Compile(&schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	// No resolver: the inlined schema must be self-contained.
	standalone, err := jsonschema.Compile(inlined)
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

			origErr := original.Validate(instance)
			inlinedErr := standalone.Validate(instance)

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

	inlined, err := jsonschema.Inline(&schema)
	require.NoError(t, err)

	original, err := jsonschema.Compile(&schema)
	require.NoError(t, err)

	standalone, err := jsonschema.Compile(inlined)
	require.NoError(t, err)

	valid := map[string]any{"p": "hello"}
	require.NoError(t, original.Validate(valid), "the original accepts a string p")
	assert.NoError(t, standalone.Validate(valid), "the inlined schema accepts a string p")

	invalid := map[string]any{"p": 5.0}
	require.Error(t, original.Validate(invalid), "the original rejects a numeric p")
	assert.Error(t, standalone.Validate(invalid), "the inlined schema rejects a numeric p")
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

			resolver := jsonschema.FileResolver(mapFS(files))

			got, err := resolver.ResolveRef(tc.uri)
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
