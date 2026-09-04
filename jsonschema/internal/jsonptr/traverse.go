package jsonptr

import (
	"github.com/google/jsonschema-go/jsonschema"
)

// TraverseSchema follows segments from schema through sub-schema keyword
// edges of the typed tree, the fast path for JSON-pointer resolution. It
// returns the schema the segments locate, or nil when some segment is not a
// typed edge ([schemafield.Subschemas]) the walk can follow. It takes the
// same typedEdge steps as [SchemaAtJSONPointer]'s typed prefix, so the two
// agree on every pointer this one answers; the caller backstops a nil by
// walking the schema's JSON form.
func TraverseSchema(schema *jsonschema.Schema, segments []string) *jsonschema.Schema {
	node := schema

	for i := 0; i < len(segments) && node != nil; {
		child, consumed, ok := typedEdge(node, segments[i:])
		if !ok {
			return nil
		}

		node = child
		i += consumed
	}

	return node
}
