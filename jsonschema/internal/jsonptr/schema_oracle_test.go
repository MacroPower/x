package jsonptr_test

import (
	"encoding/json/v2"
	"strconv"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// oraclePosition mirrors the production walk's schema-position tracking for
// the oracle below.
type oraclePosition int

const (
	oraclePosSchema oraclePosition = iota
	oraclePosSchemaMap
	oraclePosSchemaSlice
	oraclePosData
)

func oracleNextPosition(pos oraclePosition, seg string, child any) oraclePosition {
	if pos == oraclePosSchemaMap || pos == oraclePosSchemaSlice {
		return oraclePosSchema
	}

	if pos != oraclePosSchema {
		return oraclePosData
	}

	for _, f := range schemafield.Subschemas {
		if f.Keyword != seg {
			continue
		}

		switch f.Shape {
		case schemafield.Map:
			if _, ok := child.(map[string]any); ok {
				return oraclePosSchemaMap
			}

		case schemafield.Slice:
			if _, ok := child.([]any); ok {
				return oraclePosSchemaSlice
			}

		case schemafield.Single:
			switch child.(type) {
			case map[string]any, bool:
				return oraclePosSchema
			}

		default:
		}
	}

	return oraclePosData
}

// schemaAtJSONPointerOracle is the previous SchemaAtJSONPointer
// implementation, kept verbatim as the behavioral oracle: marshal the whole
// root, decode it exactly, and walk the JSON form for every segment. The
// production walk must agree with it on every pointer over a root whose whole
// tree marshals cleanly (the production walk is deliberately more permissive
// over a root carrying a marshal-fatal fault off the traversed path).
func schemaAtJSONPointerOracle(
	root *jsonschema.Schema, segments []string, base string, trackIDs bool,
	materialize jsonptr.Materialize,
) (*jsonschema.Schema, string) {
	data, err := json.Marshal(root)
	if err != nil {
		return nil, ""
	}

	node, err := decodeExact(data)
	if err != nil {
		return nil, ""
	}

	pos := oraclePosSchema

	for i, seg := range segments {
		if trackIDs && i > 0 && pos == oraclePosSchema {
			if obj, ok := node.(map[string]any); ok {
				if id, ok := obj["$id"].(string); ok && id != "" && !uriref.IsFragmentOnly(id) {
					base = uriref.StripFragment(uriref.ResolveURI(base, id))
				}
			}
		}

		switch container := node.(type) {
		case map[string]any:
			next, ok := container[seg]
			if !ok {
				return nil, ""
			}

			pos = oracleNextPosition(pos, seg, next)
			node = next

		case []any:
			idx, ok := jsonptr.ParseArrayIndex(seg)
			if !ok || idx >= len(container) {
				return nil, ""
			}

			pos = oracleNextPosition(pos, seg, container[idx])
			node = container[idx]

		default:
			return nil, ""
		}
	}

	switch node.(type) {
	case map[string]any, bool:
		schema, err := materialize(node)
		if err != nil {
			return nil, ""
		}

		return schema, base

	default:
		return nil, ""
	}
}

// materializeDeterministic is materializeSchema with deterministic member
// ordering, so the two walks' independent re-marshals of a raw member
// (Default) cannot differ by v2's randomized map order and the differential
// compares content, not ordering luck.
func materializeDeterministic(node any) (*jsonschema.Schema, error) {
	data, err := json.Marshal(node, json.Deterministic(true))
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

// enumeratePointers lists every pointer path realizable in the decoded JSON
// form of a root, each prefix included, so the differential probes every node
// the JSON walk can reach.
func enumeratePointers(node any, prefix []string, out *[][]string) {
	path := make([]string, len(prefix))
	copy(path, prefix)

	*out = append(*out, path)

	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			enumeratePointers(child, append(prefix, k), out)
		}

	case []any:
		for i, child := range v {
			enumeratePointers(child, append(prefix, strconv.Itoa(i)), out)
		}
	}
}

// TestSchemaAtJSONPointerMatchesOracle runs the production walk and the
// whole-root oracle over every realizable pointer of each root, plus
// unrealizable probes, under both trackIDs settings, and requires identical
// (schema, base) answers. The roots cover the shapes that leave the typed
// tree: Extra-carried subschemas, $id chains inside and outside unknown
// keywords, examples/default/const internals, Vocabulary and bool-field
// targets, DependencyStrings, and non-JSON-shaped Extra values.
func TestSchemaAtJSONPointerMatchesOracle(t *testing.T) {
	t.Parallel()

	subSchema := &jsonschema.Schema{Type: "string"}

	roots := map[string]*jsonschema.Schema{
		"typed graph": {
			ID:   "https://example.com/root",
			Type: "object",
			Defs: map[string]*jsonschema.Schema{
				"Foo": {Type: "string", ID: "https://example.com/foo"},
				"a/b": {Type: "integer"},
			},
			Properties: map[string]*jsonschema.Schema{
				"p": {Items: &jsonschema.Schema{Type: "number"}},
			},
			PrefixItems: []*jsonschema.Schema{{Type: "integer"}, {Type: "boolean"}},
			AllOf:       []*jsonschema.Schema{{Not: &jsonschema.Schema{}}},
		},
		"id chain through defs": {
			ID: "https://example.com/root",
			Defs: map[string]*jsonschema.Schema{
				"a": {
					ID: "nested/a.json",
					Defs: map[string]*jsonschema.Schema{
						"b": {ID: "b.json", Type: "string"},
					},
				},
			},
		},
		"unknown keyword payloads": {
			ID: "https://example.com/root",
			Extra: map[string]any{
				"myext": map[string]any{
					"$id":   "https://example.com/inert",
					"const": jsonv1.Number("9007199254740993"),
					"deep":  map[string]any{"const": 0.1},
				},
				"mylist":   []any{map[string]any{"type": "string"}, true, "s"},
				"myschema": subSchema,
				"myraw":    jsonv1.RawMessage(`{"n":1}`),
				"myfloat":  3.5,
			},
		},
		"value keyword internals": {
			Const:    func() *any { v := any(map[string]any{"k": jsonv1.Number("1")}); return &v }(),
			Enum:     []any{map[string]any{"type": "string"}, jsonv1.Number("1"), true},
			Examples: []any{map[string]any{"nested": []any{map[string]any{}}}},
			Default:  jsonv1.RawMessage(`{"if":{"type":"null"},"n":9007199254740993}`),
		},
		"data-terminal fields": {
			Type:              "object",
			ReadOnly:          true,
			WriteOnly:         true,
			Deprecated:        true,
			UniqueItems:       true,
			Required:          []string{"a"},
			DependentRequired: map[string][]string{"a": {"b"}},
			Vocabulary:        map[string]bool{"https://example.com/vocab": true},
			DependencyStrings: map[string][]string{"x": {"y"}},
			DependencySchemas: map[string]*jsonschema.Schema{"s": {Type: "string"}},
		},
		"items single form": {
			Items: &jsonschema.Schema{Type: "number"},
		},
		"items array form": {
			ItemsArray: []*jsonschema.Schema{{Type: "number"}, {Type: "string"}},
		},
		"nil subschema entries": {
			AllOf:      []*jsonschema.Schema{nil, {Type: "string"}},
			Properties: map[string]*jsonschema.Schema{"gone": nil},
		},
		"empty containers": {
			Properties: map[string]*jsonschema.Schema{},
			AllOf:      []*jsonschema.Schema{},
		},
	}

	for name, root := range roots {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(root)
			require.NoError(t, err, "oracle roots must marshal cleanly")

			decoded, err := decodeExact(data)
			require.NoError(t, err)

			var pointers [][]string

			enumeratePointers(decoded, nil, &pointers)

			// Unrealizable probes: misses, container terminals reached past
			// the end, and the index spellings RFC 6901 rejects.
			realizable := len(pointers)
			for i := range realizable {
				pointers = append(pointers, append(append([]string{}, pointers[i]...), "zzz"))
			}

			pointers = append(pointers,
				[]string{"nope"},
				[]string{"properties"},
				[]string{"items", "01"},
				[]string{"items", "-"},
				[]string{"allOf", "1", "type"},
				[]string{"$id"},
			)

			for _, trackIDs := range []bool{true, false} {
				for _, ptr := range pointers {
					wantSchema, wantBase := schemaAtJSONPointerOracle(
						root, ptr, "https://example.com/root", trackIDs, materializeDeterministic,
					)
					gotSchema, gotBase := jsonptr.SchemaAtJSONPointer(
						root, ptr, "https://example.com/root", trackIDs, materializeDeterministic,
					)

					assert.Equal(t, wantBase, gotBase, "base for %v trackIDs=%v", ptr, trackIDs)
					assert.Equal(t, wantSchema, gotSchema, "schema for %v trackIDs=%v", ptr, trackIDs)
				}
			}
		})
	}
}

// decodeExact decodes data with the exact-number discipline and returns the
// legacy any form the oracle walks.
func decodeExact(data []byte) (any, error) {
	v, err := jsonvalue.Decode(data)
	if err != nil {
		return nil, err
	}

	return v.Interface(), nil
}
