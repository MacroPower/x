// Package schemavet is the single structural-vetting policy applied to every
// schema document the compiler and the runtime accept, and the mint for the
// vetted-currency types that prove a schema passed it. A [Vetter] runs the
// field-structure check, the type-name check, the non-negative-bounds check,
// and (only when [Profile.RejectItemsArray] is set, i.e. under a draft where
// the array form of items is invalid) the items-array check, in that order, so
// the first violation a document carries is the one reported. [Vetter.VetDoc]
// adds the identifier checks ($id domain and $vocabulary placement) for call
// sites that hold a whole document with a known base URI.
//
// The currency types [Doc] and [Node] carry unexported fields and are minted
// only by [Vetter.VetDoc] and [Vetter.Vet], so a function that demands one
// cannot receive a schema that skipped vetting: the compiler enforces the
// invariant that raw *Schema values stop at the package boundary (the public
// API, the fetch closures, and the compile-time reference walk, which
// registers a fetched document and vets it before compilation returns).
// Compile and Inline hold every document to the policy. The inliner vets its
// own root and each caller-supplied fallback substitute the way Compile vets a
// root, so the two refuse the same documents for the same sentinels, except
// under the inliner's inert-$id profile, where the $id domain check does not
// run.
package schemavet

import (
	"github.com/google/jsonschema-go/jsonschema"
)

// Schema is the upstream schema type this package vets.
type Schema = jsonschema.Schema

// Profile carries the draft policy the checks read: the parent package's
// draftProfile has many more fields, and the conversion (vetProfile in
// draft.go) narrows it to the three the vetting checks consult, mirroring how
// refresolve carries its own two-value Draft enum.
type Profile struct {
	// RejectItemsArray reports whether the array form of items is a
	// structural error (Draft 2020-12, where tuples use prefixItems).
	RejectItemsArray bool
	// RejectIDFragment reports whether a fragment in $id is a structural
	// error (Draft 2020-12; under Draft-07 it is the anchor spelling).
	RejectIDFragment bool
	// Vocabularies reports whether the $vocabulary concept applies
	// (Draft 2020-12; Draft-07 predates it).
	Vocabularies bool
	// InertIDs reports whether the run reads $id as an inert annotation
	// (the inliner's WithRetrievalBase), where an $id establishes no base
	// URI and registers no resolution target. The keyword then addresses
	// nothing, so its domain is outside this policy and the identifier pass
	// skips it, as the resolution walk does.
	InertIDs bool
	// Draft7 reports whether the run resolves under Draft-07, which
	// [Freeze]'s identifier walk reads: a fragment-only $id is the anchor
	// spelling, $anchor and $dynamicAnchor are unknown keywords, and a
	// sibling $id does not change the base a $ref resolves against.
	Draft7 bool
}

// Doc is the currency minted by [Frozen.Vet] and [Vetter.VetDoc]: proof that
// a whole schema document, identifiers included, passed the vetting policy.
// The zero value holds no schema and is inert.
type Doc struct {
	root *Schema
	f    *Frozen
}

// Root returns the vetted document's root, or nil for the zero value.
func (d Doc) Root() *Schema { return d.root }

// Frozen returns the tree the document was vetted from, or nil where the
// document was vetted in place.
func (d Doc) Frozen() *Frozen { return d.f }

// Node is the currency minted by [Frozen.VetNode] and [Vetter.Vet]: proof
// that a schema fragment (a JSON-pointer fallback target, which has no
// document base of its own) passed the structural checks. The zero value
// holds no schema and is inert.
type Node struct {
	root *Schema
	f    *Frozen
}

// Root returns the vetted fragment, or nil for the zero value.
func (n Node) Root() *Schema { return n.root }

// Frozen returns the tree the fragment was vetted from, or nil where the
// fragment was vetted in place.
func (n Node) Frozen() *Frozen { return n.f }

// Vetter runs the structural-vetting policy. The visited sets guard
// schema-graph cycles and let one vetter deduplicate across several passes;
// Compile shares a single vetter over the root, the fallback targets, and the
// fetched remotes, so a node reached both locally and through a remote URI is
// checked once and its violation is attributed to the pass that reached it
// first. A fetch reached only at validation or inline time builds a fresh
// vetter per document, since each such document is independent.
type Vetter struct {
	structVisited map[*Schema]bool
	typeVisited   map[*Schema]bool
	boundsVisited map[*Schema]bool
	itemsVisited  map[*Schema]bool
	idVisited     map[*Schema]bool
	profile       Profile
}

// NewVetter returns a vetter with fresh visited sets, carrying the run's
// [Profile] policy. The profile's RejectItemsArray flag gates the items-array
// check, and its RejectIDFragment and Vocabularies flags feed the identifier
// checks of [Vetter.VetDoc]; the structure, type-name, and bounds checks are
// draft-agnostic.
func NewVetter(profile Profile) *Vetter {
	return &Vetter{
		structVisited: map[*Schema]bool{},
		typeVisited:   map[*Schema]bool{},
		boundsVisited: map[*Schema]bool{},
		itemsVisited:  map[*Schema]bool{},
		idVisited:     map[*Schema]bool{},
		profile:       profile,
	}
}

// Vet applies the structural checks to s, prefixing each check's traversal
// with pathPrefix so a violation names the offending document exactly. It
// returns the minted [Node] on success, or the zero Node and the first
// violation.
func (v *Vetter) Vet(s *Schema, pathPrefix string) (Node, error) {
	err := checkSchemaStructure(s, pathPrefix, v.structVisited)
	if err != nil {
		return Node{}, err
	}

	err = checkTypeNames(s, pathPrefix, v.typeVisited)
	if err != nil {
		return Node{}, err
	}

	err = checkBoundDomains(s, pathPrefix, v.boundsVisited)
	if err != nil {
		return Node{}, err
	}

	if v.profile.RejectItemsArray {
		err = checkItemsArrayDraft2020(s, pathPrefix, v.itemsVisited)
		if err != nil {
			return Node{}, err
		}
	}

	return Node{root: s}, nil
}

// VetDoc applies [Vetter.Vet] plus the identifier checks ($id domain and
// $vocabulary placement, see checkIdentifiers) to a document rooted at s,
// whose base URI is base. It serves the call sites that hold a whole
// document with a known base: the root at Compile, each registry-known
// document, each fetched document, the inliner's root, and a fallback
// substitute, which enters resolution space as a document of its own. A
// JSON-pointer fallback target keeps the plain [Vetter.Vet]; it is a
// fragment of a document already checked, and no document base is in hand at
// its location. It returns the minted [Doc] on success, or the zero Doc
// and the first violation.
func (v *Vetter) VetDoc(s *Schema, pathPrefix, base string) (Doc, error) {
	_, err := v.Vet(s, pathPrefix)
	if err != nil {
		return Doc{}, err
	}

	err = checkIdentifiers(s, pathPrefix, base, v.profile, v.idVisited)
	if err != nil {
		return Doc{}, err
	}

	return Doc{root: s}, nil
}

// CheckTypeNames verifies that every type keyword reachable from schema names
// one of the seven JSON Schema type names, returning nil or an error wrapping
// [ErrInvalidType] that includes the schema path of the first offending
// keyword. It is the standalone form behind the parent package's public
// CheckTypeNames: it runs only the type-name walk with a fresh visited map,
// so a schema carrying both a structural conflict and a bad type name reports
// the type name, the same first error the public wrapper reports. A nil
// schema returns nil.
func CheckTypeNames(schema *Schema) error {
	return checkTypeNames(schema, "", map[*Schema]bool{})
}
