package jsonptr

import (
	"github.com/google/jsonschema-go/jsonschema"
)

// TraverseSchema follows segments from schema through sub-schema keyword
// edges of the typed tree, the fast path for JSON-pointer resolution. It
// returns the schema the segments locate, or nil when some segment is not a
// typed edge ([schemafield.Subschemas]) the walk can follow. It is the
// all-or-nothing form of [TypedPrefix], the same walk [SchemaAtJSONPointer]
// runs, so the two agree on every pointer this one answers; the caller
// backstops a nil by walking the schema's JSON form.
func TraverseSchema(schema *jsonschema.Schema, segments []string) *jsonschema.Schema {
	node, rest, _ := TypedPrefix(schema, segments, "", false)
	if len(rest) > 0 {
		return nil
	}

	return node
}
