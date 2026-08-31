package jsonptr

import (
	"encoding/json/v2"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
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

// SchemaAtJSONPointer navigates root by segments and returns the value located
// there, materialized as a Schema when it is itself a schema (a JSON object or
// boolean), or nil otherwise. The walk descends the typed tree while each
// segment follows a sub-schema keyword edge ([schemafield.Subschemas]), and
// switches to JSON form at the first segment that does not. The node standing
// there alone is marshaled and decoded with [normalize.UnmarshalExact], so
// numbers survive as exact [encoding/json.Number] literals without re-encoding
// the whole root, and the located node is handed to materialize as a fresh
// copy unaliased from root. A node with a marshal-fatal keyword combination
// (both items forms set, for one) therefore refuses the walk only where the
// walk still marshals, at the node the JSON form starts from or below it; a
// fault elsewhere in the document leaves other pointers unaffected.
//
// The walk starts from base (root's base URI) and, when trackIDs is set,
// tracks $id members of the crossed objects that occupy schema positions --
// the typed nodes of the prefix, and below the switch the sub-schema keyword
// containers the [schemafield.Subschemas] table declares -- so the returned
// base is the one in effect at the located target; the target's own $id is
// left to the caller during registration. A "$id" string inside a non-schema
// keyword's payload (examples, default, const) or an unknown keyword is plain
// instance data, never a resource boundary, and leaves base untouched; a
// target reached through such data keeps the base of its nearest enclosing
// schema resource. A caller whose walk treats $id as inert (a retrieval-base
// walk) passes trackIDs false, and every crossed $id leaves base untouched.
func SchemaAtJSONPointer(
	root *jsonschema.Schema, segments []string, base string, trackIDs bool,
	materialize Materialize,
) (*jsonschema.Schema, string) {
	node := root

	i := 0
	for i < len(segments) && node != nil {
		// Crossing into an intermediate schema that establishes a resource
		// ($id) rebases everything below it. The walk skips the starting
		// root, whose $id is already reflected in base. The rebase runs before
		// the edge is attempted, mirroring the JSON-form walk, which rebases
		// on the object it stands on whatever the segment then resolves to.
		if trackIDs && i > 0 && node.ID != "" && !uriref.IsFragmentOnly(node.ID) {
			base = uriref.IDBase(base, node.ID)
		}

		child, consumed, ok := typedEdge(node, segments[i:])
		if !ok {
			break
		}

		node = child
		i += consumed
	}

	return schemaAtJSONForm(node, segments[i:], base, trackIDs, materialize)
}

// typedEdge descends one sub-schema keyword edge of the typed tree, consuming
// the keyword segment plus, for a map- or slice-shaped keyword, the member
// segment after it. It declines (ok=false) whatever it cannot follow as a
// typed edge -- a data keyword, an absent member, a nil child, a keyword
// whose two field forms are both set (items) -- and the caller answers those
// from the node's JSON form, which is the arbiter the edge must agree with.
func typedEdge(node *jsonschema.Schema, segs []string) (*jsonschema.Schema, int, bool) {
	var (
		child    *jsonschema.Schema
		consumed int
		set      int
	)

	for _, f := range schemafield.Subschemas {
		if f.Keyword != segs[0] {
			continue
		}

		switch f.Shape {
		case schemafield.Single:
			s := f.SingleOf(node)
			if s == nil {
				continue
			}

			set++
			child, consumed = s, 1

		case schemafield.Map:
			m := f.MapOf(node)
			if m == nil {
				continue
			}

			set++

			if len(segs) > 1 {
				if s, ok := m[segs[1]]; ok && s != nil {
					child, consumed = s, 2
				}
			}

		case schemafield.Slice:
			s := f.SliceOf(node)
			if s == nil {
				continue
			}

			set++

			if len(segs) > 1 {
				if idx, ok := ParseArrayIndex(segs[1]); ok && idx < len(s) && s[idx] != nil {
					child, consumed = s[idx], 2
				}
			}

		default: // None never appears in Subschemas.
		}
	}

	// Two set forms of one keyword (items) marshal fatally; declining hands
	// the conflict to the JSON form, which refuses it as the marshal does.
	if set != 1 || child == nil {
		return nil, 0, false
	}

	return child, consumed, true
}

// schemaAtJSONForm finishes [SchemaAtJSONPointer] from the node the typed
// prefix stopped at: the node marshals alone, decodes with the exact-number
// discipline, and the remaining segments descend the JSON form with $id
// tracking picking up where the typed prefix left off (the node's own $id is
// already applied there, so the local walk rebases only below it).
func schemaAtJSONForm(
	schema *jsonschema.Schema, segments []string, base string, trackIDs bool,
	materialize Materialize,
) (*jsonschema.Schema, string) {
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, ""
	}

	// Decode with the exact-number discipline so a number beyond float64
	// precision keeps its literal form: the materialized target's const/enum
	// must hold what the author wrote, not the rounded float64 neighbor.
	node, err := normalize.DecodeJSONInstance(data)
	if err != nil {
		return nil, ""
	}

	pos := posSchema

	for i, seg := range segments {
		if trackIDs && i > 0 && pos == posSchema {
			if obj, ok := node.(map[string]any); ok {
				if id, ok := obj["$id"].(string); ok && id != "" && !uriref.IsFragmentOnly(id) {
					base = uriref.IDBase(base, id)
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
