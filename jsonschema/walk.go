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
	refs := SubschemaRefs(s)
	if len(refs) == 0 {
		return nil
	}

	children := make([]*Schema, len(refs))
	for i, ref := range refs {
		children[i] = ref.Schema
	}

	return children
}

// SubschemaRef pairs one direct sub-schema with the RFC 6901 JSON Pointer
// addressing it from its parent.
type SubschemaRef struct {
	// Schema is the child schema.
	Schema *Schema

	// Pointer is the JSON Pointer from the parent schema to Schema: the
	// keyword token plus, for map and list keywords, the escaped member key
	// or the element index (for example "/properties/a", "/allOf/0",
	// "/items"). Appending each visited child's Pointer while descending
	// yields the schema path the package's own errors report.
	Pointer string
}

// SubschemaRefs is the keyword-labeled form of [Subschemas]: the same
// children in the same order, each paired with the JSON Pointer addressing
// it from s, so path-tracking traversals need not re-derive which keyword
// holds each child. [Subschemas] delegates here, so traversals that pair
// children position by position and traversals that track paths can never
// disagree on field coverage or order. A nil s returns nil.
func SubschemaRefs(s *Schema) []SubschemaRef {
	if s == nil {
		return nil
	}

	var children []SubschemaRef

	for _, entry := range []struct {
		m       map[string]*Schema
		keyword string
	}{
		{s.Properties, keywordProperties},
		{s.PatternProperties, keywordPatternProperties},
		{s.Defs, keywordDefs},
		{s.Definitions, keywordDefinitions},
		{s.DependentSchemas, keywordDependentSchemas},
		{s.DependencySchemas, keywordDependencies},
	} {
		for _, key := range slices.Sorted(maps.Keys(entry.m)) {
			if sub := entry.m[key]; sub != nil {
				children = append(children, SubschemaRef{
					Pointer: "/" + entry.keyword + "/" + escapeJSONPointer(key),
					Schema:  sub,
				})
			}
		}
	}

	for _, entry := range []struct {
		keyword string
		list    []*Schema
	}{
		{keywordAllOf, s.AllOf},
		{keywordAnyOf, s.AnyOf},
		{keywordOneOf, s.OneOf},
		{keywordPrefixItems, s.PrefixItems},
		{keywordItems, s.ItemsArray},
	} {
		for i, sub := range entry.list {
			if sub != nil {
				children = append(children, SubschemaRef{
					Pointer: "/" + entry.keyword + "/" + strconv.Itoa(i),
					Schema:  sub,
				})
			}
		}
	}

	for _, entry := range []struct {
		s       *Schema
		keyword string
	}{
		{s.Items, keywordItems},
		{s.AdditionalProperties, keywordAdditionalProperties},
		{s.AdditionalItems, keywordAdditionalItems},
		{s.Not, keywordNot},
		{s.If, keywordIf},
		{s.Then, keywordThen},
		{s.Else, keywordElse},
		{s.Contains, keywordContains},
		{s.PropertyNames, keywordPropertyNames},
		{s.UnevaluatedProperties, keywordUnevaluatedProperties},
		{s.UnevaluatedItems, keywordUnevaluatedItems},
		{s.ContentSchema, keywordContentSchema},
	} {
		if entry.s != nil {
			children = append(children, SubschemaRef{
				Pointer: "/" + entry.keyword,
				Schema:  entry.s,
			})
		}
	}

	return children
}

// Walk calls fn for s and every schema transitively reachable through
// [Subschemas], pre-order: fn runs on a schema before its children are
// gathered, so fn may replace or mutate sub-schema fields and the walk
// follows the updated children. Each distinct schema pointer is visited
// once, so aliased or cyclic graphs terminate. Walk stops at and returns
// the first error from fn, except [SkipChildren], which prunes the walk at
// that schema and continues. A nil s is a no-op.
//
// Walk is [WalkRefs] without the path.
func Walk(s *Schema, fn func(*Schema) error) error {
	return WalkRefs(s, func(_ string, s *Schema) error { return fn(s) })
}

// WalkRefs is [Walk] with path tracking: fn also receives the RFC 6901 JSON
// Pointer addressing each visited schema from s (the root itself is ""),
// built by appending each descended child's [SubschemaRef.Pointer], so it
// matches the schema path the package's own errors report. The [Walk]
// semantics apply unchanged: pre-order with the walk following updated
// children, one visit per distinct schema pointer, [SkipChildren] pruning,
// and stop at the first error. A schema reachable through several paths is
// visited with the first path the traversal encounters; [SubschemaRefs]
// orders map-held children by sorted key, so that path is deterministic.
// A nil s is a no-op.
func WalkRefs(s *Schema, fn func(path string, s *Schema) error) error {
	return walkRefs(s, "", fn, map[*Schema]bool{})
}

// walkRefs implements [WalkRefs], threading the visited set through the
// recursion so each distinct schema pointer runs fn at most once. A pruned
// schema stays visited: another path reaching it later finds it handled,
// exactly as if the walk had descended through it.
func walkRefs(s *Schema, path string, fn func(string, *Schema) error, visited map[*Schema]bool) error {
	if s == nil || visited[s] {
		return nil
	}

	visited[s] = true

	err := fn(path, s)
	if errors.Is(err, SkipChildren) {
		return nil
	}

	if err != nil {
		return err
	}

	for _, ref := range SubschemaRefs(s) {
		err := walkRefs(ref.Schema, path+ref.Pointer, fn, visited)
		if err != nil {
			return err
		}
	}

	return nil
}
