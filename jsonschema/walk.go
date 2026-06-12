package jsonschema

import (
	"errors"
	"maps"
	"slices"
	"strconv"
)

// SkipChildren is returned by a [Walk] function to prune the walk at the
// current schema: its sub-schemas are not visited, the walk continues with
// the schema's siblings, and Walk does not treat it as an error. Returned
// from the root, Walk visits only the root. It follows the [io/fs.SkipDir]
// convention.
//
//nolint:errname,staticcheck // A control-flow sentinel, not a failure; named for its meaning, like io/fs.SkipDir.
var SkipChildren = errors.New("skip this schema's children")

// Location is the position of a schema within a containing schema document,
// in the two synchronized forms the package uses everywhere (mirroring how
// validation errors carry [ValidationError.InstancePath] alongside
// [ValidationError.InstanceSegments]): the RFC 6901 JSON Pointer string and
// the typed [Segment] slice. The zero value addresses the root. [Walk]
// passes one to its callback, and [SubschemaEntry] carries one per child;
// appending a child's Location to its parent's while descending yields the
// schema path the package's own errors report.
type Location struct {
	// Pointer is the RFC 6901 JSON Pointer ("" addresses the root). Member
	// keys carry ~0/~1 escaping.
	Pointer string

	// Segments is the typed form of Pointer, one [Segment] per reference
	// token (nil addresses the root). Unlike Pointer, a member key is
	// carried verbatim — no ~0/~1 escaping to undo — and a list index is
	// distinguished from a property named like a number, so consumers
	// building on the location need not re-parse the pointer string.
	Segments []Segment
}

// SubschemaEntry pairs one direct sub-schema with the embedded [Location]
// addressing it from its parent: the keyword token plus, for map and list
// keywords, the member key or the element index (for example
// "/properties/a", "/allOf/0", "/items").
type SubschemaEntry struct {
	// Schema is the child schema.
	Schema *Schema

	// Location addresses Schema from its parent.
	Location
}

// SubschemaEntries returns the direct sub-schemas of s: every non-nil schema
// reachable through one sub-schema-bearing keyword (applicators such as
// items, properties, allOf, not, if/then/else, plus $defs and definitions),
// each paired with the JSON Pointer addressing it from s. Children held in
// maps are returned in sorted-key order so traversal is deterministic. Only
// typed Schema fields are included, not sub-schemas carried as raw JSON in
// unknown keywords (the Extra map). A nil s returns nil.
//
// SubschemaEntries is the package's single source of truth for which Schema
// fields hold sub-schemas: [Walk] and the internal traversals build on it,
// and a maintenance test fails when an upstream Schema addition is not
// covered.
func SubschemaEntries(s *Schema) []SubschemaEntry {
	if s == nil {
		return nil
	}

	var children []SubschemaEntry

	for _, entry := range []struct {
		m       map[string]*Schema
		keyword string
	}{
		{s.Properties, KeywordProperties},
		{s.PatternProperties, KeywordPatternProperties},
		{s.Defs, KeywordDefs},
		{s.Definitions, KeywordDefinitions},
		{s.DependentSchemas, KeywordDependentSchemas},
		{s.DependencySchemas, KeywordDependencies},
	} {
		for _, key := range slices.Sorted(maps.Keys(entry.m)) {
			if sub := entry.m[key]; sub != nil {
				children = append(children, SubschemaEntry{
					Location: Location{
						Pointer:  "/" + entry.keyword + "/" + escapeJSONPointer(key),
						Segments: []Segment{{Key: entry.keyword}, {Key: key}},
					},
					Schema: sub,
				})
			}
		}
	}

	for _, entry := range []struct {
		keyword string
		list    []*Schema
	}{
		{KeywordAllOf, s.AllOf},
		{KeywordAnyOf, s.AnyOf},
		{KeywordOneOf, s.OneOf},
		{KeywordPrefixItems, s.PrefixItems},
		{KeywordItems, s.ItemsArray},
	} {
		for i, sub := range entry.list {
			if sub != nil {
				children = append(children, SubschemaEntry{
					Location: Location{
						Pointer:  "/" + entry.keyword + "/" + strconv.Itoa(i),
						Segments: []Segment{{Key: entry.keyword}, {Index: i, IsIndex: true}},
					},
					Schema: sub,
				})
			}
		}
	}

	for _, entry := range []struct {
		s       *Schema
		keyword string
	}{
		{s.Items, KeywordItems},
		{s.AdditionalProperties, KeywordAdditionalProperties},
		{s.AdditionalItems, KeywordAdditionalItems},
		{s.Not, KeywordNot},
		{s.If, KeywordIf},
		{s.Then, KeywordThen},
		{s.Else, KeywordElse},
		{s.Contains, KeywordContains},
		{s.PropertyNames, KeywordPropertyNames},
		{s.UnevaluatedProperties, KeywordUnevaluatedProperties},
		{s.UnevaluatedItems, KeywordUnevaluatedItems},
		{s.ContentSchema, KeywordContentSchema},
	} {
		if entry.s != nil {
			children = append(children, SubschemaEntry{
				Location: Location{
					Pointer:  "/" + entry.keyword,
					Segments: []Segment{{Key: entry.keyword}},
				},
				Schema: entry.s,
			})
		}
	}

	return children
}

// Walk calls fn for s and every schema transitively reachable through
// [SubschemaEntries], pre-order: fn runs on a schema before its children are
// gathered, so fn may replace or mutate sub-schema fields and the walk
// follows the updated children. Each distinct schema pointer is visited
// once, so aliased or cyclic graphs terminate. Walk stops at and returns
// the first error from fn, except [SkipChildren], which prunes the walk at
// that schema and continues. A nil s is a no-op.
//
// Fn receives each visited schema's [Location] within s (the zero Location
// for the root itself), built by appending each descended child's
// [SubschemaEntry] location. The pointer matches the schema path the
// package's own errors report; the segments carry member keys verbatim and
// distinguish list indexes from numeric-looking keys, so fn need not
// re-parse the pointer. Fn must not mutate loc.Segments. A traversal with no
// use for the location ignores the parameter, following [io/fs.WalkDir].
// A schema reachable through several paths is visited with the first path
// the traversal encounters; [SubschemaEntries] orders map-held children by
// sorted key, so that path is deterministic.
func Walk(s *Schema, fn func(loc Location, s *Schema) error) error {
	return walkPaths(s, Location{}, fn, map[*Schema]bool{})
}

// walkPaths implements [Walk], threading the visited set through the
// recursion so each distinct schema pointer runs fn at most once. A pruned
// schema stays visited: another path reaching it later finds it handled,
// exactly as if the walk had descended through it.
func walkPaths(
	s *Schema,
	loc Location,
	fn func(Location, *Schema) error,
	visited map[*Schema]bool,
) error {
	if s == nil || visited[s] {
		return nil
	}

	visited[s] = true

	err := fn(loc, s)
	if errors.Is(err, SkipChildren) {
		return nil
	}

	if err != nil {
		return err
	}

	for _, entry := range SubschemaEntries(s) {
		childLoc := Location{
			Pointer: loc.Pointer + entry.Pointer,
			// Concat allocates a fresh backing array per child, so sibling
			// descents never alias the slices fn may have retained.
			Segments: slices.Concat(loc.Segments, entry.Segments),
		}

		err := walkPaths(entry.Schema, childLoc, fn, visited)
		if err != nil {
			return err
		}
	}

	return nil
}
