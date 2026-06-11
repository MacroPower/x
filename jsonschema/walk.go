package jsonschema

import (
	"maps"
	"slices"
)

// Subschemas returns the direct sub-schemas of s: every non-nil schema
// reachable through one sub-schema-bearing keyword (applicators such as
// items, properties, allOf, not, if/then/else, plus $defs and definitions).
// Children held in maps are returned in sorted-key order so traversal is
// deterministic. Only typed Schema fields are included, not sub-schemas
// carried as raw JSON in unknown keywords (the Extra map). A nil s returns
// nil.
//
// Subschemas is the package's single source of truth for which Schema fields
// hold sub-schemas: [Walk] and the internal traversals build on it, and a
// maintenance test fails when an upstream Schema addition is not covered.
func Subschemas(s *Schema) []*Schema {
	if s == nil {
		return nil
	}

	var children []*Schema

	for _, m := range []map[string]*Schema{
		s.Properties, s.PatternProperties, s.Defs, s.Definitions,
		s.DependentSchemas, s.DependencySchemas,
	} {
		for _, key := range slices.Sorted(maps.Keys(m)) {
			if sub := m[key]; sub != nil {
				children = append(children, sub)
			}
		}
	}

	for _, list := range [][]*Schema{
		s.AllOf, s.AnyOf, s.OneOf, s.PrefixItems, s.ItemsArray,
	} {
		for _, sub := range list {
			if sub != nil {
				children = append(children, sub)
			}
		}
	}

	for _, sub := range []*Schema{
		s.Items, s.AdditionalProperties, s.AdditionalItems, s.Not,
		s.If, s.Then, s.Else, s.Contains, s.PropertyNames,
		s.UnevaluatedProperties, s.UnevaluatedItems, s.ContentSchema,
	} {
		if sub != nil {
			children = append(children, sub)
		}
	}

	return children
}

// Walk calls fn for s and every schema transitively reachable through
// [Subschemas], pre-order: fn runs on a schema before its children are
// gathered, so fn may replace or mutate sub-schema fields and the walk
// follows the updated children. Each distinct schema pointer is visited
// once, so aliased or cyclic graphs terminate. Walk stops at and returns
// the first error from fn. A nil s is a no-op.
func Walk(s *Schema, fn func(*Schema) error) error {
	return walk(s, fn, map[*Schema]bool{})
}

// walk implements [Walk], threading the visited set through the recursion so
// each distinct schema pointer runs fn at most once.
func walk(s *Schema, fn func(*Schema) error, visited map[*Schema]bool) error {
	if s == nil || visited[s] {
		return nil
	}

	visited[s] = true

	err := fn(s)
	if err != nil {
		return err
	}

	for _, child := range Subschemas(s) {
		err := walk(child, fn, visited)
		if err != nil {
			return err
		}
	}

	return nil
}
