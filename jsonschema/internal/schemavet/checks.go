package schemavet

import (
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/keywordmeta"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// draft2020SchemaURI is the Draft 2020-12 $schema URI, the one dialect whose
// metaschemas may carry $vocabulary. The parent package's Draft.schemaURI()
// owns the canonical value; a parent-side guard test pins this copy to it,
// since this package cannot import the parent.
const draft2020SchemaURI = "https://json-schema.org/draft/2020-12/schema"

// Entry is one direct sub-schema of a node with the location addressing it
// from that node, both as the JSON Pointer the vetting errors embed and as the
// reference tokens the parent package's typed segments carry verbatim.
type Entry struct {
	// Schema is the child schema.
	Schema *Schema

	// Pointer is the RFC 6901 JSON Pointer addressing Schema from its parent:
	// the keyword token plus, for a map or slice keyword, the escaped member
	// key or the element index.
	Pointer jsontext.Pointer

	// Keyword is the sub-schema keyword the child sits under.
	Keyword string

	// Key is the unescaped member key under a map keyword.
	Key string

	// Index is the element index under a slice keyword.
	Index int

	// Shape is the keyword's container shape, which says whether Key or
	// Index carries the second token: [schemafield.Map] for Key,
	// [schemafield.Slice] for Index, [schemafield.Single] for neither.
	Shape schemafield.Shape
}

// Entries returns the direct sub-schemas of s with their locations: the
// [schemafield.Subschemas] field list in its pinned order, sorted map keys
// escaped by [jsonptr.AppendToken], /keyword/N for slices, and the single
// Items form skipped when ItemsArray is populated, since both express the
// items keyword and a hand-built schema may set both. It is the one
// traversal behind the parent package's SubschemaEntries, which adds the
// typed segments, and behind every vetting check, whose violations embed the
// pointers, so a location reads the same wherever the package reports it.
func Entries(s *Schema) []Entry {
	if s == nil {
		return nil
	}

	var children []Entry

	for _, f := range schemafield.Subschemas {
		switch f.Shape {
		case schemafield.None:
			// Subschemas never contains a None field; the case is here only to
			// keep the switch exhaustive over Shape.

		case schemafield.Map:
			m := f.MapOf(s)
			for _, key := range slices.Sorted(maps.Keys(m)) {
				if sub := m[key]; sub != nil {
					children = append(children, Entry{
						Schema:  sub,
						Pointer: jsonptr.AppendToken(jsontext.Pointer("/"+f.Keyword), key),
						Keyword: f.Keyword,
						Shape:   f.Shape,
						Key:     key,
					})
				}
			}

		case schemafield.Slice:
			for i, sub := range f.SliceOf(s) {
				if sub != nil {
					children = append(children, Entry{
						Schema:  sub,
						Pointer: jsontext.Pointer("/" + f.Keyword + "/" + strconv.Itoa(i)),
						Keyword: f.Keyword,
						Shape:   f.Shape,
						Index:   i,
					})
				}
			}

		case schemafield.Single:
			sub := f.SingleOf(s)
			if sub == nil {
				continue
			}

			// Items and ItemsArray are the two mutually exclusive forms of the
			// items keyword; when ItemsArray is populated (emitted above as
			// /items/N), skip the single Items form so the keyword does not
			// also yield a contradictory /items pointer for the same location.
			if f.Name == "Items" && len(s.ItemsArray) > 0 {
				continue
			}

			children = append(children, Entry{
				Schema:  sub,
				Pointer: jsontext.Pointer("/" + f.Keyword),
				Keyword: f.Keyword,
				Shape:   f.Shape,
			})
		}
	}

	return children
}

// checkTypeNames verifies that every type keyword reachable from schema names
// one of the seven JSON Schema types, returning an error wrapping
// [ErrInvalidType] for the first violation. The traversal uses [Entries] for
// the sub-schema field list, appending each entry's Pointer so the error
// locates the offending keyword; visited guards against schema graph cycles.
// The check is draft-agnostic: neither draft defines type names beyond the
// canonical seven.
func checkTypeNames(schema *Schema, schemaPath string, visited map[*Schema]bool) error {
	if schema == nil || visited[schema] {
		return nil
	}

	visited[schema] = true

	if schema.Type != "" && !typename.Valid(schema.Type) {
		return fmt.Errorf("%w: %q at %s/type", ErrInvalidType, schema.Type, schemaPath)
	}

	for _, name := range schema.Types {
		if !typename.Valid(name) {
			return fmt.Errorf("%w: %q at %s/type", ErrInvalidType, name, schemaPath)
		}
	}

	for _, entry := range Entries(schema) {
		err := checkTypeNames(entry.Schema, schemaPath+string(entry.Pointer), visited)
		if err != nil {
			return err
		}
	}

	return nil
}

// checkSchemaStructure rejects a schema whose Go representation cannot express
// one coherent JSON document: a keyword spelled through both of its Go fields
// (Type and Types, Defs and Definitions, Items and ItemsArray, a dependencies
// key in both DependencySchemas and DependencyStrings), a duplicate
// PropertyOrder entry, and a nil *Schema element inside a sub-schema slice or
// map. Each conflicting pair marshals to a single JSON keyword, so the walk
// would silently prefer one form; a nil container element is skipped by the
// walk, so the branch the author listed would assert nothing. The traversal
// mirrors [checkTypeNames]: it uses [Entries] for the recursion and each
// entry's Pointer for the location, with visited guarding schema-graph
// cycles. The nil-element scan reads the raw containers through the canonical
// [schemafield.Subschemas] table, since [Entries] itself skips nil elements.
func checkSchemaStructure(schema *Schema, schemaPath string, visited map[*Schema]bool) error {
	if schema == nil || visited[schema] {
		return nil
	}

	visited[schema] = true

	err := checkFieldConflicts(schema, schemaPath)
	if err != nil {
		return err
	}

	err = checkNilSubschemaEntries(schema, schemaPath)
	if err != nil {
		return err
	}

	for _, entry := range Entries(schema) {
		err := checkSchemaStructure(entry.Schema, schemaPath+string(entry.Pointer), visited)
		if err != nil {
			return err
		}
	}

	return nil
}

// checkFieldConflicts reports the per-node field conflicts of
// [checkSchemaStructure]: the three both-fields-set pairs, a dependencies key
// present in both maps, and a duplicate PropertyOrder entry.
func checkFieldConflicts(schema *Schema, schemaPath string) error {
	if schema.Type != "" && schema.Types != nil {
		return fmt.Errorf("%w: both Type and Types at %s/type", ErrConflictingSchemaFields, schemaPath)
	}

	if schema.Defs != nil && schema.Definitions != nil {
		return fmt.Errorf("%w: both Defs and Definitions at %s/$defs", ErrConflictingSchemaFields, schemaPath)
	}

	if schema.Items != nil && schema.ItemsArray != nil {
		return fmt.Errorf("%w: both Items and ItemsArray at %s/items", ErrConflictingSchemaFields, schemaPath)
	}

	if len(schema.DependencyStrings) > 0 {
		for _, key := range slices.Sorted(maps.Keys(schema.DependencySchemas)) {
			if _, ok := schema.DependencyStrings[key]; ok {
				return fmt.Errorf("%w: dependencies key %q is both a schema and a string array at %s/dependencies/%s",
					ErrConflictingSchemaFields, key, schemaPath, jsonptr.Escape(key))
			}
		}
	}

	seen := make(map[string]bool, len(schema.PropertyOrder))
	for _, name := range schema.PropertyOrder {
		if seen[name] {
			return fmt.Errorf("%w: %q at %s/propertyOrder", ErrDuplicatePropertyOrder, name, schemaPath)
		}

		seen[name] = true
	}

	return nil
}

// checkNilSubschemaEntries reports the first nil *Schema element inside one of
// the node's sub-schema slices or maps. The scan reads the raw containers via
// the [schemafield.Subschemas] table rather than [Entries], which skips nil
// elements by contract; map elements are scanned in sorted-key order so the
// reported violation is deterministic.
func checkNilSubschemaEntries(schema *Schema, schemaPath string) error {
	for _, f := range schemafield.Subschemas {
		switch f.Shape {
		case schemafield.None, schemafield.Single:
			// A nil single field is an absent keyword, not a violation.

		case schemafield.Slice:
			for i, sub := range f.SliceOf(schema) {
				if sub == nil {
					return fmt.Errorf("%w at %s/%s/%d", ErrNilSubschema, schemaPath, f.Keyword, i)
				}
			}

		case schemafield.Map:
			m := f.MapOf(schema)
			for _, key := range slices.Sorted(maps.Keys(m)) {
				if m[key] == nil {
					return fmt.Errorf("%w at %s/%s/%s", ErrNilSubschema, schemaPath, f.Keyword, jsonptr.Escape(key))
				}
			}
		}
	}

	return nil
}

// checkIdentifiers verifies the $id and $vocabulary domains over a schema
// document, threading the enclosing base URI exactly as the registry walk
// does, so a violation is judged against the same base the resolution
// machinery would use. Per node it checks the $vocabulary placement (see
// [ErrMisplacedVocabulary]) and, when the node declares an $id the run reads
// as live, its domain (see [ErrInvalidID] and [checkSchemaID], which also
// computes the base the node's children inherit). Under [Profile.InertIDs]
// checkIdentifiers skips the $id pass, since an inert $id names no target.
// The traversal mirrors [checkTypeNames]: it uses [Entries] for the
// recursion and each entry's Pointer for the location, with visited guarding
// schema-graph cycles.
func checkIdentifiers(
	schema *Schema, schemaPath, base string, profile Profile, visited map[*Schema]bool,
) error {
	if schema == nil || visited[schema] {
		return nil
	}

	visited[schema] = true

	err := checkVocabularyPlacement(schema, schemaPath, profile)
	if err != nil {
		return err
	}

	currentBase := base

	// An inert-$id run registers no $id and rebases no child, so the keyword
	// addresses nothing and the base stays the document's retrieval URI. The
	// skip mirrors refresolve's own walk, which passes the same $id over.
	if schema.ID != "" && !profile.InertIDs {
		next, err := checkSchemaID(schema, schemaPath, currentBase, profile)
		if err != nil {
			return err
		}

		currentBase = next
	}

	for _, entry := range Entries(schema) {
		err := checkIdentifiers(entry.Schema, schemaPath+string(entry.Pointer), currentBase, profile, visited)
		if err != nil {
			return err
		}
	}

	return nil
}

// checkVocabularyPlacement rejects a $vocabulary on a node whose $schema does
// not establish the Draft 2020-12 dialect. A non-empty $schema must be
// exactly the 2020-12 URI (spec section 8.1.2 defines $vocabulary for that
// dialect's metaschemas; any other dialect string, including the
// trailing-"#" spelling, cannot carry one). An empty $schema is read as
// inheriting the run's dialect: accepted under the Draft 2020-12 profile,
// rejected under Draft-07, which predates the vocabulary concept.
func checkVocabularyPlacement(schema *Schema, schemaPath string, profile Profile) error {
	if schema.Vocabulary == nil {
		return nil
	}

	if schema.Schema == "" {
		if profile.Vocabularies {
			return nil
		}

		return fmt.Errorf("%w: $vocabulary under the Draft-07 dialect at %s/$vocabulary",
			ErrMisplacedVocabulary, schemaPath)
	}

	if schema.Schema != draft2020SchemaURI {
		return fmt.Errorf("%w: $vocabulary under $schema %q at %s/$vocabulary",
			ErrMisplacedVocabulary, schema.Schema, schemaPath)
	}

	return nil
}

// checkSchemaID checks one node's non-empty $id against the keyword's domain
// and returns the base URI the node's children inherit. The base threads
// exactly as in the registry walk (refresolve.Registry's walkInto): a
// fragment-only $id changes no base, and any other $id rebases the children
// to [uriref.IDBase] of itself against the enclosing base, whether or not its
// checks ran. Under Draft-07 two forms go unchecked: an $id beside a $ref
// (the draft ignores it) and a fragment-carrying $id (the anchor spelling);
// Draft 2020-12 rejects any fragment in $id (core section 8.2.1). A checked
// $id must parse, and its resolved form must be an absolute URI; a relative
// $id with no absolute base registers no resolvable URI, so every ref
// targeting it would silently miss.
func checkSchemaID(schema *Schema, schemaPath, base string, profile Profile) (string, error) {
	id := schema.ID

	if uriref.IsFragmentOnly(id) {
		if profile.RejectIDFragment {
			return "", fmt.Errorf("%w: $id %q must not carry a fragment at %s/$id",
				ErrInvalidID, id, schemaPath)
		}

		// Draft-07 anchor form: an anchor registration, no base change.
		return base, nil
	}

	resolved := uriref.IDBase(base, id)

	// Draft-07 ignores an $id beside a $ref, so its domain goes unchecked;
	// the resolved base still threads to the children, as in the registry
	// walk.
	if !profile.RejectIDFragment && schema.Ref != "" {
		return resolved, nil
	}

	parsed, err := url.Parse(id)
	if err != nil {
		return "", fmt.Errorf("%w: cannot parse $id %q at %s/$id", ErrInvalidID, id, schemaPath)
	}

	if parsed.Fragment != "" {
		if profile.RejectIDFragment {
			return "", fmt.Errorf("%w: $id %q must not carry a fragment at %s/$id",
				ErrInvalidID, id, schemaPath)
		}

		// Draft-07 fragment-carrying $id: the anchor reading, unchecked.
		return resolved, nil
	}

	resolvedURL, err := url.Parse(resolved)
	if err != nil || !resolvedURL.IsAbs() {
		return "", fmt.Errorf("%w: $id %q does not resolve to an absolute URI against base %q at %s/$id",
			ErrInvalidID, id, base, schemaPath)
	}

	return resolved, nil
}

// checkItemsArrayDraft2020 rejects the Draft-07 array form of the items keyword
// (ItemsArray, what upstream parses a JSON `"items": [ ... ]` into) when
// compiling under Draft 2020-12, where it has no meaning. Without this the
// 2020-12 array walk drops the constraint silently and validates every element
// against nothing. The traversal mirrors [checkTypeNames]: it uses [Entries]
// for the field list and each entry's Pointer for the location, with visited
// guarding schema-graph cycles. The caller gates it on
// [Profile.RejectItemsArray], so Draft-07 schemas pay nothing.
func checkItemsArrayDraft2020(schema *Schema, schemaPath string, visited map[*Schema]bool) error {
	if schema == nil || visited[schema] {
		return nil
	}

	visited[schema] = true

	// A nil check rather than a length check: upstream unmarshals a present
	// but empty `"items": []` into a non-nil empty slice, and that array form
	// is just as meaningless under 2020-12 (it silently drops the Draft-07
	// semantics of its additionalItems sibling).
	if schema.ItemsArray != nil {
		return fmt.Errorf("%w; use prefixItems at %s/items", ErrItemsArrayUnderDraft2020, schemaPath)
	}

	for _, entry := range Entries(schema) {
		err := checkItemsArrayDraft2020(entry.Schema, schemaPath+string(entry.Pointer), visited)
		if err != nil {
			return err
		}
	}

	return nil
}

// The sizeBounds table lists the length and count keywords with their
// *int accessors, for the compile-time domain check in
// [checkBoundDomains]. The init guard below pins the list to
// [keywordmeta.Sizes], so a count keyword added to the semantics table
// cannot silently skip the check.
var sizeBounds = []struct {
	get     func(*Schema) *int
	keyword string
}{
	{func(s *Schema) *int { return s.MinLength }, keyword.MinLength},
	{func(s *Schema) *int { return s.MaxLength }, keyword.MaxLength},
	{func(s *Schema) *int { return s.MinItems }, keyword.MinItems},
	{func(s *Schema) *int { return s.MaxItems }, keyword.MaxItems},
	{func(s *Schema) *int { return s.MinProperties }, keyword.MinProperties},
	{func(s *Schema) *int { return s.MaxProperties }, keyword.MaxProperties},
	{func(s *Schema) *int { return s.MinContains }, keyword.MinContains},
	{func(s *Schema) *int { return s.MaxContains }, keyword.MaxContains},
}

// init cross-checks sizeBounds against the semantics table's derived size
// set. Panicking at load follows the dispatch table's convention: every test
// binary in the module trips it (the parent package imports this one
// unconditionally), so the drift a hand-maintained keyword list invites
// cannot land silently.
func init() {
	declared := make([]string, 0, len(sizeBounds))
	for _, b := range sizeBounds {
		declared = append(declared, b.keyword)
	}

	slices.Sort(declared)

	if derived := keywordmeta.Names(keywordmeta.Sizes); !slices.Equal(declared, derived) {
		panic(fmt.Sprintf(
			"schemavet: sizeBounds (%v) does not match keywordmeta.Sizes (%v)",
			declared, derived,
		))
	}
}

// checkBoundDomains rejects a keyword value outside the domain the spec fixes
// for it: a negative value on a length or count keyword (each defined as a
// non-negative integer) and a non-positive multipleOf (defined as a number
// strictly greater than zero). An invalid value would otherwise silently
// mis-validate: a negative maximum rejects every instance, a negative minimum
// never fires, and a non-positive multipleOf rejects every numeric instance
// while accepting every non-numeric one. The traversal mirrors
// [checkTypeNames]: it uses [Entries] for the field list and each entry's
// Pointer for the location, with visited guarding schema-graph cycles. It is
// draft-agnostic; every draft fixes these domains identically.
func checkBoundDomains(schema *Schema, schemaPath string, visited map[*Schema]bool) error {
	if schema == nil || visited[schema] {
		return nil
	}

	visited[schema] = true

	for _, bound := range sizeBounds {
		if value := bound.get(schema); value != nil && *value < 0 {
			return fmt.Errorf("%w: %s is %d at %s/%s",
				ErrNegativeBound, bound.keyword, *value, schemaPath, bound.keyword)
		}
	}

	if schema.MultipleOf != nil && *schema.MultipleOf <= 0 {
		return fmt.Errorf("%w: got %v at %s/%s",
			ErrNonPositiveMultipleOf, *schema.MultipleOf, schemaPath, keyword.MultipleOf)
	}

	for _, entry := range Entries(schema) {
		err := checkBoundDomains(entry.Schema, schemaPath+string(entry.Pointer), visited)
		if err != nil {
			return err
		}
	}

	return nil
}
