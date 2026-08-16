package jsonptr

import (
	"bytes"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// position classifies where the JSON-form walk stands relative to the schema
// tree, so $id tracking rebases only at objects that are themselves schemas. A
// "$id" string inside a non-schema keyword's payload (examples, default,
// const) or an unknown keyword is plain instance data, not a resource
// boundary, and must not rebase the walk.
type position int

const (
	// A posSchema node is itself a schema.
	posSchema position = iota

	// A posSchemaMap node is the container of a map-shaped sub-schema keyword
	// (properties, $defs, ...); its values are schemas.
	posSchemaMap

	// A posSchemaSlice node is the container of a slice-shaped sub-schema
	// keyword (allOf, prefixItems, ...); its elements are schemas.
	posSchemaSlice

	// A posData node is plain instance data; nothing below it is a schema
	// position, so no $id below it rebases.
	posData
)

// subschemaShapes maps each sub-schema keyword to the container shapes it can
// hold, derived from the canonical [schemafield.Subschemas] table so the
// JSON-form walk cannot disagree with the typed field table about which
// keywords hold schemas. A keyword with two rows (items) carries both shapes
// and disambiguates by the value's actual JSON form.
var subschemaShapes = func() map[string][]schemafield.Shape {
	m := make(map[string][]schemafield.Shape)

	for _, f := range schemafield.Subschemas {
		m[f.Keyword] = append(m[f.Keyword], f.Shape)
	}

	return m
}()

// nextPosition classifies the node reached by descending from a parent at pos
// through segment seg, matching each candidate shape of a sub-schema keyword
// against the child's actual JSON form.
func nextPosition(pos position, seg string, child any) position {
	switch pos {
	case posSchema:
		for _, shape := range subschemaShapes[seg] {
			switch shape {
			case schemafield.Map:
				if _, ok := child.(map[string]any); ok {
					return posSchemaMap
				}

			case schemafield.Slice:
				if _, ok := child.([]any); ok {
					return posSchemaSlice
				}

			case schemafield.Single:
				switch child.(type) {
				case map[string]any, bool:
					return posSchema
				}

			default: // None never appears in Subschemas.
			}
		}

		return posData

	case posSchemaMap, posSchemaSlice:
		// The values of a schema map and the elements of a schema slice are
		// schema positions.
		return posSchema

	case posData:
		return posData

	default:
		return posData
	}
}

// Materialize converts a located JSON value (a map[string]any or bool, with
// [json.Number] leaves) into a Schema. The caller supplies it because building
// a Schema whose const/enum numbers stay exact requires the parent package's
// decode discipline, which this package cannot name without an import cycle.
type Materialize func(node any) (*jsonschema.Schema, error)

// SchemaAtJSONPointer navigates root's JSON encoding by segments and returns
// the value located there, materialized as a Schema when it is itself a schema
// (a JSON object or boolean), or nil otherwise. The JSON form decodes with
// UseNumber so numbers survive as exact [json.Number] literals, and the
// located node is handed to materialize, keeping a const or enum beyond
// float64 precision exact in the returned schema.
//
// The walk starts from base (root's base URI) and, when trackIDs is set,
// tracks $id members of the crossed objects that occupy schema positions --
// the sub-schema keyword containers the [schemafield.Subschemas] table
// declares -- so the returned base is the one in effect at the located
// target; the target's own $id is left to the caller during registration. A
// "$id" string inside a non-schema keyword's payload (examples, default,
// const) or an unknown keyword is plain instance data, never a resource
// boundary, and leaves base untouched; a target reached through such data
// keeps the base of its nearest enclosing schema resource. A caller whose
// walk treats $id as inert (a retrieval-base walk) passes trackIDs false, and
// every crossed $id leaves base untouched.
func SchemaAtJSONPointer(
	root *jsonschema.Schema, segments []string, base string, trackIDs bool,
	materialize Materialize,
) (*jsonschema.Schema, string) {
	data, err := json.Marshal(root)
	if err != nil {
		return nil, ""
	}

	// Decode with UseNumber so a number beyond float64 precision keeps its
	// literal form: the materialized target's const/enum must hold what the
	// author wrote, not the rounded float64 neighbor.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var node any

	err = dec.Decode(&node)
	if err != nil {
		return nil, ""
	}

	pos := posSchema

	for i, seg := range segments {
		// Crossing into an intermediate schema that establishes a resource
		// ($id) rebases everything below it. Only an object in a schema
		// position rebases: a "$id" data member inside a non-schema keyword
		// is inert. The starting root is skipped: its own $id is already
		// reflected in base.
		if trackIDs && i > 0 && pos == posSchema {
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

			pos = nextPosition(pos, seg, next)
			node = next

		case []any:
			idx, ok := ParseArrayIndex(seg)
			if !ok || idx >= len(container) {
				return nil, ""
			}

			pos = nextPosition(pos, seg, container[idx])
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
