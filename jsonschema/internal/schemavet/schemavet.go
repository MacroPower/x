// Package schemavet is the single structural-vetting policy applied to every
// schema document the compiler and the runtime accept, and the mint for the
// vetted-currency types that prove a schema passed it. [Freeze] copies a
// document into a private tree, refusing a pointer cycle, and reads its
// identifiers into the tables the resolution registry is built from. The
// [Frozen] tree then vets: [Frozen.VetNode] runs the field-structure check,
// the type-name check, the non-negative-bounds check, and (only when
// [Profile.RejectItemsArray] is set, i.e. under a draft where the array form
// of items is invalid) the items-array check, in that order, so the first
// violation a document carries is the one reported. [Frozen.Vet] adds the
// identifier checks ($id domain and $vocabulary placement) for call sites
// that hold a whole document with a known base URI.
//
// The currency types [Doc] and [Node] carry unexported fields and are minted
// only by [Frozen.Vet] and [Frozen.VetNode], so a function that demands one
// cannot receive a schema that skipped the freeze or the vet: the compiler
// enforces the invariant that raw *Schema values stop at the package boundary
// (the public API, the fetch closures, and the fallback vet). Compile and
// Inline hold every document to the policy. The inliner freezes and vets its
// own root and each caller-supplied fallback substitute the way Compile does
// a root, so the two refuse the same documents for the same sentinels, except
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

// Doc is the currency minted by [Frozen.Vet]: proof that a whole schema
// document, identifiers included, was frozen and passed the vetting policy.
// The zero value holds no schema and is inert.
type Doc struct {
	root *Schema
	f    *Frozen
}

// Root returns the vetted document's root, or nil for the zero value.
func (d Doc) Root() *Schema { return d.root }

// Frozen returns the tree the document was vetted from, or nil for the zero
// value.
func (d Doc) Frozen() *Frozen { return d.f }

// Node is the currency minted by [Frozen.VetNode]: proof that a schema
// fragment (a JSON-pointer fallback target, which has no document base of
// its own) was frozen and passed the structural checks. [Node.Narrow]
// re-roots it at a node below the fragment's root. The zero value holds no
// schema and is inert.
type Node struct {
	root *Schema
	f    *Frozen
}

// Root returns the vetted fragment, or nil for the zero value.
func (n Node) Root() *Schema { return n.root }

// Frozen returns the tree the fragment was vetted from, or nil for the zero
// value.
func (n Node) Frozen() *Frozen { return n.f }

// Narrow returns the currency rooted at s, a node of the vetted tree, and
// whether s is one. The checks ran over the whole tree, so every subtree
// passed them, and the narrowed proof names the fragment a caller reached
// below the root: a node an $id or $anchor inside the tree registered, which
// a reference resolves to without naming the root. Its [Node.Frozen] is the
// whole tree.
func (n Node) Narrow(s *Schema) (Node, bool) {
	if _, ok := n.f.ID(s); !ok {
		return Node{}, false
	}

	return Node{root: s, f: n.f}, true
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
