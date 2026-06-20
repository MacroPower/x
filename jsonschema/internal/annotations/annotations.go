// Package annotations tracks which object properties and array items a schema
// has evaluated during one validation, the bookkeeping that
// unevaluatedProperties and unevaluatedItems consult.
//
// A Set is the JSON Schema 2020-12 annotation collection (core section 10.x):
// the set of evaluated property names, the set of matched item indexes, the
// prefix/items watermark, and the two "all evaluated" saturation flags. The
// merge rule (union the sets, OR the flags, take the larger watermark) lives
// here; the policy of WHEN a subschema's annotations roll up into its parent --
// allOf only on whole-allOf success, anyOf per matching branch, oneOf the
// single match, not and a failed if/then/else contributing nothing -- stays
// with the validator that orchestrates the walk.
//
// Every method is nil-receiver-safe: a nil *Set is an untracked collection that
// reads as empty and ignores writes, so a parent not collecting annotations
// (its schema has no unevaluated* keyword to satisfy) needs no guard at each
// call site.
package annotations

// Set is an annotation collection for one schema node: the properties and items
// it evaluated during validation.
type Set struct {
	properties    map[string]bool
	itemIndexes   map[int]bool
	itemsEnd      int
	allProperties bool
	allItems      bool
}

// New returns an empty Set ready to record evaluations.
func New() *Set {
	return &Set{
		properties:  map[string]bool{},
		itemIndexes: map[int]bool{},
	}
}

// Child returns a fresh Set when s tracks annotations, or nil when it does not.
// A composition, conditional, reference, or dependency keyword collects a
// subschema's evaluation only to merge it back into s; when s is nil that
// result is discarded, so skipping the allocation avoids two maps per subschema
// on schemas with no unevaluatedProperties/Items to satisfy.
func (s *Set) Child() *Set {
	if s == nil {
		return nil
	}

	return New()
}

// Merge folds other's evaluations into s: the union of evaluated properties and
// matched item indexes, the larger items watermark, and the OR of the
// saturation flags. A nil s or nil other is a no-op, so an untracked parent or
// an un-collected child contributes nothing.
func (s *Set) Merge(other *Set) {
	if s == nil || other == nil {
		return
	}

	for k := range other.properties {
		s.properties[k] = true
	}

	if other.allProperties {
		s.allProperties = true
	}

	if other.itemsEnd > s.itemsEnd {
		s.itemsEnd = other.itemsEnd
	}

	for k := range other.itemIndexes {
		s.itemIndexes[k] = true
	}

	if other.allItems {
		s.allItems = true
	}
}

// RecordProperty marks the property name as evaluated.
func (s *Set) RecordProperty(name string) {
	if s == nil {
		return
	}

	s.properties[name] = true
}

// SetAllProperties marks every property of the instance as evaluated.
func (s *Set) SetAllProperties() {
	if s == nil {
		return
	}

	s.allProperties = true
}

// RecordItem marks the item at index i as evaluated.
func (s *Set) RecordItem(i int) {
	if s == nil {
		return
	}

	s.itemIndexes[i] = true
}

// SetAllItems marks every item of the instance as evaluated.
func (s *Set) SetAllItems() {
	if s == nil {
		return
	}

	s.allItems = true
}

// ExtendItems advances the evaluated-prefix watermark to end when end is larger,
// recording that every item below end has been evaluated (the prefixItems and
// single-schema items annotation).
func (s *Set) ExtendItems(end int) {
	if s == nil {
		return
	}

	if end > s.itemsEnd {
		s.itemsEnd = end
	}
}

// Evaluated reports whether the property name has been evaluated.
func (s *Set) Evaluated(name string) bool {
	if s == nil {
		return false
	}

	return s.properties[name]
}

// ItemEvaluated reports whether the item at index i has been evaluated, either
// because it falls below the prefix watermark or because a contains subschema
// matched it.
func (s *Set) ItemEvaluated(i int) bool {
	if s == nil {
		return false
	}

	return i < s.itemsEnd || s.itemIndexes[i]
}

// AllPropertiesSet reports whether every property has been marked evaluated.
func (s *Set) AllPropertiesSet() bool {
	if s == nil {
		return false
	}

	return s.allProperties
}

// AllItemsSet reports whether every item has been marked evaluated.
func (s *Set) AllItemsSet() bool {
	if s == nil {
		return false
	}

	return s.allItems
}
