// Package schemaclone deep-copies a [jsonschema.Schema] via a JSON round-trip,
// then restores the render-only PropertyOrder field the round-trip drops. The
// validation walk and the inliner both need an independent copy of a schema
// whose in-place mutation cannot corrupt the caller's value, so the copy logic
// is centralized here as a single source of truth.
package schemaclone

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// Children returns the direct sub-schemas of a schema in a stable order. [Clone]
// walks src and its copy in lockstep through one such function to pair nodes; the
// jsonschema package supplies its SubschemaEntries traversal in this shape.
type Children func(*jsonschema.Schema) []*jsonschema.Schema

// Clone deep-copies s via a JSON round-trip and returns the copy.
//
// Upstream [jsonschema.Schema.CloneSchemas] is shallow for non-sub-schema fields
// (Extra, Enum, Const, Default, Examples): it shares their backing maps, slices,
// and pointers with the original. A round-trip through JSON instead yields an
// independent copy of every serializable field, which is what remote-ref
// isolation requires so [jsonschema.Schema.Resolve]'s in-place mutations cannot
// corrupt the caller's schema. The render-only PropertyOrder field carries
// json:"-", so the round-trip drops it; it is restored afterward (via children)
// so a clone preserves property ordering. Every other serializable field
// round-trips as an independent copy.
func Clone(s *jsonschema.Schema, children Children) (*jsonschema.Schema, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("clone schema: %w", err)
	}

	var cp jsonschema.Schema

	err = json.Unmarshal(data, &cp)
	if err != nil {
		return nil, fmt.Errorf("clone schema: %w", err)
	}

	restorePropertyOrder(s, &cp, children)

	return &cp, nil
}

// restorePropertyOrder copies the render-only PropertyOrder field (json:"-", so
// dropped by [Clone]'s JSON round-trip) from src onto cp at every node, walking
// both in lockstep through children. Because cp is a JSON clone of src, the two
// share an identical sub-schema structure and children (which orders map-held
// entries deterministically) yields matching orders. Each slice is cloned so cp
// stays unaliased from src. A JSON clone is always a finite tree, so the
// recursion terminates.
func restorePropertyOrder(src, cp *jsonschema.Schema, children Children) {
	if src == nil || cp == nil {
		return
	}

	if src.PropertyOrder != nil {
		cp.PropertyOrder = slices.Clone(src.PropertyOrder)
	}

	srcChildren := children(src)
	cpChildren := children(cp)

	if len(srcChildren) != len(cpChildren) {
		return // Structural mismatch; nothing safe to pair.
	}

	for i := range srcChildren {
		restorePropertyOrder(srcChildren[i], cpChildren[i], children)
	}
}
