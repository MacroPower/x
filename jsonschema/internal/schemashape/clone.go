package schemashape

import (
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
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
// caller's exact typed values. Every Schema field whose type is []any, []string,
// map[string]bool, map[string][]string, [json.RawMessage], *any, or
// map[string]any is covered here; TestTypeSchemaOverrideContainersUnaliased in
// the jsonschema package's generate_test.go fails if a future upstream field of
// one of those types is added without being cloned.
func CloneOverrideExtras(s *jsonschema.Schema) {
	if s.Enum != nil {
		s.Enum = slices.Clone(s.Enum)
	}

	if s.Const != nil {
		c := *s.Const
		s.Const = &c
	}

	if s.Default != nil {
		s.Default = slices.Clone(s.Default)
	}

	if s.Extra != nil {
		s.Extra = maps.Clone(s.Extra)
	}

	if s.Examples != nil {
		s.Examples = slices.Clone(s.Examples)
	}

	if s.Required != nil {
		s.Required = slices.Clone(s.Required)
	}

	if s.Types != nil {
		s.Types = slices.Clone(s.Types)
	}

	if s.PropertyOrder != nil {
		s.PropertyOrder = slices.Clone(s.PropertyOrder)
	}

	if s.Vocabulary != nil {
		s.Vocabulary = maps.Clone(s.Vocabulary)
	}

	if s.DependencyStrings != nil {
		s.DependencyStrings = maps.Clone(s.DependencyStrings)
	}

	if s.DependentRequired != nil {
		s.DependentRequired = maps.Clone(s.DependentRequired)
	}
}
