package tagmodel

import "github.com/google/jsonschema-go/jsonschema"

// Target is one write destination: what is being constrained, where the
// authored facts land, and what the type already declared. A field and an
// element are the same type here, which is what makes retargeting a
// sequence-wide rule onto elements one implementation rather than two.
type Target struct {
	// Canvas is where authored facts land.
	Canvas *jsonschema.Schema
	// Base is the type-derived schema, read-only: what the type itself already
	// declares, consulted so a rule that would silently overwrite it conflicts
	// instead.
	Base *jsonschema.Schema
	// The elems closure supplies the element targets lazily, and is nil when
	// the shape has none. It is the seam that lets each caller build elements
	// from what it has: the generator wraps its node-backed element accessor,
	// while the jsonschema tag pairs the canvas item slots with the payload.
	elems func() []Target
	// The refBase closure lazily supplies the schema of the definition Base
	// defers to, and is nil when Base is not a reference (or the caller cannot
	// follow one). It is a seam like elems: the model cannot resolve a $ref
	// itself, so the caller that can supplies the read, and the effective-value
	// checks consult it so a value the definition declares outright stays
	// visible through the reference.
	refBase func() *jsonschema.Schema
	// Shape is the JSON shape being constrained.
	Shape Shape
}

// NewTarget builds a write destination. Elems may be nil for a shape with no
// element schemas.
func NewTarget(sh Shape, canvas, base *jsonschema.Schema, elems func() []Target) Target {
	return Target{Shape: sh, Canvas: canvas, Base: base, elems: elems}
}

// WithRefBase returns the target reading the referenced definition's
// type-derived schema through fn. The generator sets it on a $ref-payload
// target so a string keyword the definition declares outright is not invisible
// to the first-wins composition gate ([KeywordFirstWins]): an interpreter's
// inferred keyword would otherwise land as a $ref sibling and conjoin with the
// definition's own, which for two disjoint formats no instance can satisfy.
func (t Target) WithRefBase(fn func() *jsonschema.Schema) Target {
	t.refBase = fn

	return t
}

// Elements returns the target's element destinations, or nil when it has none.
// A nil result means "no item schema to constrain", which the rejecting cells
// report rather than silently dropping the rule.
func (t Target) Elements() []Target {
	if t.elems == nil {
		return nil
	}

	return t.elems()
}

// recursesIntoItself reports an element whose own type is the container's, the
// shape a self-referential sequence (type T []*T) takes. Retargeting onto it
// would descend forever, so the caller names the real cause instead of bottoming
// out on a misleading "no item schema".
func (t Target) recursesIntoItself(elem Target) bool {
	return elem.Shape.Elem == t.Shape.Elem
}
