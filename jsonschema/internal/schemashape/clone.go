package schemashape

import (
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
)

// CloneOverrideExtras clones the non-sub-schema container fields that the
// upstream CloneSchemas leaves aliased to the source schema. CloneSchemas
// deep-copies only the sub-schema fields (*Schema, []*Schema, map[string]*Schema)
// and shallow-shares every other reference field, so an extender or interpreter
// that appends or assigns into one of those in place would corrupt the caller's
// schema across Generate calls.
//
// The policy is top-level headers only: each slice, map, and pointer container
// is reallocated so writes to it cannot reach the source, but the nested any
// values and the bytes they reference keep their identity, preserving the
// caller's exact typed values. The set of cloned fields lives in the canonical
// [go.jacobcolvin.com/x/jsonschema/internal/schemafield] table (every field
// carrying a CloneContainer closure); TestTypeSchemaOverrideContainersUnaliased
// in the jsonschema package's generate_test.go fails if a future upstream field
// of one of those types is added without being cloned.
func CloneOverrideExtras(s *jsonschema.Schema) {
	schemafield.CloneContainers(s)
}
