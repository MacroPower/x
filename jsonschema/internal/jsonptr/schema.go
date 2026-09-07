package jsonptr

import (
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
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
// [encoding/json.Number] leaves) into a Schema. The caller supplies it because building
// a Schema whose const/enum numbers stay exact requires the parent package's
// decode discipline, which this package cannot name without an import cycle.
type Materialize func(node any) (*jsonschema.Schema, error)

// SchemaAtJSONForm navigates schema by segments through its JSON form and
// returns the value located there, materialized as a Schema when it is
// itself a schema (a JSON object or boolean), or nil otherwise. The schema
// round-trips through [jsonvalue.Exact] alone, so numbers survive as exact
// [encoding/json.Number] literals without re-encoding any enclosing document,
// and the located node is handed to materialize as a fresh copy unaliased
// from schema. A schema with a marshal-fatal keyword combination (both items
// forms set, for one) refuses the walk, while a fault elsewhere in the
// enclosing document leaves it unaffected; a caller that has already
// followed the typed prefix of a pointer through its own tables (the
// resolver session over its frozen trees) therefore passes the deepest node
// it reached and the segments left below it rather than the document root,
// which also spares the walk a second pass over the prefix.
//
// The walk starts from base, the base URI in effect at schema, and, when
// rebase is non-nil, applies it to each crossed object that occupies a schema
// position -- the sub-schema keyword containers the [schemafield.Subschemas]
// table declares -- so the returned base is the one in effect at the located
// target. The rebase callback reads the object's own $id against the running
// base, the same reading [schemavet.ScopeOfJSON] gives a typed node, and
// returns the base its children inherit. Base already carries schema's own
// $id, so the walk rebases only below it; the target's own $id is left to the
// caller during registration. A "$id" string inside a non-schema keyword's
// payload (examples, default, const) or an unknown keyword is plain instance
// data, never a resource boundary, and leaves base untouched; a target
// reached through such data keeps the base of its nearest enclosing schema
// resource. A caller whose walk treats $id as inert (a retrieval-base walk)
// passes a nil rebase, and every crossed $id leaves base untouched.
func SchemaAtJSONForm(
	schema *jsonschema.Schema, segments []string, base uriref.DocKey,
	rebase func(obj map[string]any, base uriref.DocKey) uriref.DocKey,
	materialize Materialize,
) (*jsonschema.Schema, uriref.DocKey) {
	// Exact is the marshal + exact-decode round trip, so a number beyond
	// float64 precision keeps its literal form: the materialized target's
	// const/enum must hold what the author wrote, not the rounded float64
	// neighbor.
	node, ok := jsonvalue.Exact(schema)
	if !ok {
		return nil, uriref.DocKey{}
	}

	pos := posSchema

	for i, seg := range segments {
		if rebase != nil && i > 0 && pos == posSchema {
			if obj, ok := node.(map[string]any); ok {
				base = rebase(obj, base)
			}
		}

		switch container := node.(type) {
		case map[string]any:
			next, ok := container[seg]
			if !ok {
				return nil, uriref.DocKey{}
			}

			pos = nextPosition(pos, seg, next)
			node = next

		case []any:
			idx, ok := ParseArrayIndex(seg)
			if !ok || idx >= len(container) {
				return nil, uriref.DocKey{}
			}

			pos = nextPosition(pos, seg, container[idx])
			node = container[idx]

		default:
			return nil, uriref.DocKey{}
		}
	}

	switch node.(type) {
	case map[string]any, bool:
		schema, err := materialize(node)
		if err != nil {
			return nil, uriref.DocKey{}
		}

		return schema, base

	default:
		return nil, uriref.DocKey{}
	}
}
