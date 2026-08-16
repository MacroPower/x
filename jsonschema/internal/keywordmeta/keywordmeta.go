// Package keywordmeta is the canonical per-keyword semantics table for the
// jsonschema generation half: one declared row per keyword name in
// internal/keyword, stating how an authored value merges with the type-derived
// one, which branch of the nullable anyOf split it lands on, and which drafts
// and vocabulary gate it.
//
// It is the keyword-side sibling of internal/schemafield, which tabulates the
// upstream Schema type's Go fields. Each row names the schemafield rows it
// claims ([Keyword.Fields]), so an upstream field addition cannot land without a
// keyword classification. The field reconciliation drives its movable,
// authorable, and bound partitions off the derived sets ([Movable], [Authored],
// [Bounds]), so a keyword's semantics live in one row rather than in three set
// memberships.
//
// This is not the validator's keyword dispatch table (keyword_table.go in the
// parent package), which groups keywords into ordered evaluation rows and owns
// their eval and precompute steps. The two are cross-checked: every keyword a
// dispatch row owns is [Keyword.Asserted] here, each dispatch row's draft range
// is the hull of its members' declared [Keyword.Drafts], and each row's
// vocabulary matches its members' declared [Keyword.Vocab], except where a
// member declares [Keyword.VocabRefined].
package keywordmeta

import (
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
)

// Schema is the upstream schema type this package's closures read and write. It
// is the same alias the parent jsonschema package exports.
type Schema = jsonschema.Schema

// Draft mirrors the parent package's Draft enum. It is redeclared here rather
// than imported (the parent imports this package, not the reverse), and both the
// ordering and the exact numeric values are load-bearing: [DraftRange] is a
// closed interval over them, and the parent converts its own Draft to this one
// raw. A sync assertion in the parent's tests pins the two enums together.
type Draft int

const (
	// Draft2020 targets JSON Schema Draft 2020-12. It mirrors the parent's
	// Draft2020 and must hold the same numeric value.
	Draft2020 Draft = 0

	// Draft7 targets JSON Schema Draft-07. It mirrors the parent's Draft7 and
	// must hold the same numeric value.
	Draft7 Draft = -100

	// DraftMin is the lower sentinel of an open-ended range, spaced far below
	// every Draft value so a range starting here genuinely means "and any older
	// draft".
	DraftMin Draft = -1 << 30

	// DraftMax is the upper sentinel of an open-ended range, spaced far above
	// every Draft value so a range ending here genuinely means "and any newer
	// draft" rather than collapsing to the newest current one.
	DraftMax Draft = 1 << 30
)

// DraftRange expresses a keyword's applicability as a closed interval over the
// ordered [Draft] values: an O(1), allocation-free membership test that is
// monotonic with draft removal (a keyword introduced at one draft and removed at
// another names the interval between them).
type DraftRange struct{ Lo, Hi Draft }

// Contains reports whether draft d falls within the closed interval.
func (r DraftRange) Contains(d Draft) bool { return d >= r.Lo && d <= r.Hi }

// Hull returns the smallest range covering both r and other.
func (r DraftRange) Hull(other DraftRange) DraftRange {
	return DraftRange{Lo: min(r.Lo, other.Lo), Hi: max(r.Hi, other.Hi)}
}

// VocabGroup names the vocabulary group that gates a keyword, matching the
// dispatch table's framing rather than the spec's vocabulary assignment:
// [VocabCore] means "no optional 2020-12 vocabulary gates this keyword", which
// holds for $ref, for the legacy dependencies form (in no 2020-12 vocabulary at
// all), and for format (whose assertion is gated by the dispatch table's
// separate opt-in field, not by a vocabulary).
type VocabGroup uint8

const (
	// VocabCore is always active: the keywords no optional vocabulary gates.
	VocabCore VocabGroup = iota

	// VocabApplicator maps to the applicator vocabulary.
	VocabApplicator

	// VocabValidation maps to the validation vocabulary.
	VocabValidation

	// VocabUnevaluated maps to the unevaluated-applicator vocabulary.
	VocabUnevaluated

	// VocabContent maps to the content vocabulary.
	VocabContent
)

// Merge is how an authored value composes with the type-derived value the
// reflection payload already carries.
//
// [MergeIntersect] keywords conjoin soundly: the authored value and the
// type-derived one may both hold at once, and the tighter of the two wins. The
// bound endpoints are the canonical case -- an authored maximum beside a
// kind-derived maximum is just the smaller of the two.
//
// [MergeReplace] keywords do not conjoin: the authored value overwrites the
// type-derived one, so holding both at once asserts something the author never
// wrote. The multipleOf, pattern, and format keywords are the standing
// examples: multipleOf 6 over a type's 4 accepts only multiples of 12, and two
// disjoint patterns reject every string. That is why a [ScopeWrapper] row may
// never be MergeReplace: the split's move-and-restore puts the authored value on
// the wrapper AND the type value back on the value branch, silently conjoining
// the two, so the non-nullable and nullable occurrences of one type would accept
// different sets from the identical tag.
//
// [MergeNone] marks a keyword whose composition carries no assertion, either
// because no dispatch row asserts it (the annotations) or because the field
// reconciliation never authors it (the structural keywords). It is definitional
// rather than a third independent state: a row merges as None exactly when it is
// [ScopeNone] or unasserted.
//
// Nothing dispatches on Merge. The overlay blind-assigns every authored keyword
// and the bound algebra does its own intersecting, so this column is a declared
// property cross-checked against [Scope], not a switch the reconciliation reads.
// Marking a new row MergeIntersect therefore does not make it conjoin; it only
// asserts that conjoining it would be sound, which is the precondition for
// riding the wrapper.
type Merge uint8

const (
	// MergeNone is the structural default: no assertion composes.
	MergeNone Merge = iota

	// MergeIntersect marks a keyword whose authored and type-derived values may
	// both hold at once.
	MergeIntersect

	// MergeReplace marks a keyword whose authored value overwrites the
	// type-derived one.
	MergeReplace
)

// Scope is the branch of the deferred null encoding's anyOf split an authored
// keyword lands on.
//
// [ScopeWrapper] keywords move onto the fresh anyOf wrapper, and the value
// branch gets its type-derived value restored beneath them. That move-and-
// restore is sound only when the two values conjoin ([MergeIntersect]) or the
// keyword asserts nothing at all, so a ScopeWrapper row may never be
// [MergeReplace].
//
// [ScopeValue] keywords ride the value branch instead, where null is never
// subject to them. Two independent reasons put a keyword here, which is why
// Scope and [Merge] are separate columns: a MergeReplace keyword must not be
// conjoined (multipleOf, pattern, format, the content keywords), and a keyword
// that would reject null must not gate the wrapper (const, and enum -- which
// conjoins soundly, yet on the wrapper would reject the null branch).
//
// [ScopeNone] marks a keyword the field reconciliation never authors: the
// structural sub-schema keywords, re-rendered from the value node instead, and
// allOf, whose slice holds a composite's embed branches (appended by renderBase)
// alongside any forbid-value conjunct -- both belong on the value branch, and a
// blind copy would destroy the embed branches.
type Scope uint8

const (
	// ScopeNone is the structural default: the reconciliation never authors it.
	ScopeNone Scope = iota

	// ScopeWrapper marks a keyword the null split moves onto the anyOf wrapper.
	ScopeWrapper

	// ScopeValue marks a keyword that stays on the anyOf's value branch.
	ScopeValue
)

// Keyword is one JSON Schema keyword's declared semantics.
//
// Field order is tuned for struct packing (govet fieldalignment); the grouping
// is not semantic.
type Keyword struct {
	// Differs reports whether the keyword holds different values in two schemas:
	// against the pristine payload it marks the keyword authored, and against an
	// empty schema (see [Keyword.Set]) it marks it set. It is nil exactly when
	// Assign is, so a keyword cannot be authorable without being comparable.
	Differs func(a, b *Schema) bool

	// Assign copies the keyword from src onto dst, overwriting dst's value (which
	// clears it when src's is the zero value). It is nil for a keyword the field
	// reconciliation never authors.
	Assign func(src, dst *Schema)

	// Name is the keyword name, one of the internal/keyword constants.
	Name string

	// Fields names the Go field(s) in [schemafield.Fields] this keyword owns.
	// A few keywords own two (type, items, and the legacy dependencies form);
	// most own one. That link carries the coverage guard: an upstream field
	// addition arrives with a new keyword, and the guard fails until this table
	// classifies it.
	Fields []string

	// Drafts is the closed draft interval in which the keyword exists. Each
	// dispatch row's range is derived as the hull of its members' Drafts.
	Drafts DraftRange

	// Merge is how an authored value composes with the type-derived one.
	Merge Merge

	// Scope is the branch of the nullable split an authored value lands on.
	Scope Scope

	// Vocab is the vocabulary group that gates the keyword. It is cross-checked
	// against the owning dispatch row's declared vocabulary.
	Vocab VocabGroup

	// Bound reports whether the keyword participates in the shared constraint
	// algebra as a numeric, length, or count endpoint.
	Bound bool

	// Size reports whether the keyword's value domain is a non-negative
	// integer (a length or count). The parent's compile-time domain check
	// derives its keyword list from this column, so a future count keyword
	// declared here cannot silently skip that check. It is inferred for the
	// int-typed bound rows and declared outright on the contains counts,
	// which carry no bound row (the tag dialects cannot author them).
	Size bool

	// Asserted reports whether a dispatch row owns the keyword -- not whether it
	// always asserts. Format, contentEncoding, and contentMediaType are Asserted
	// yet stay annotation-only until their row's opt-in gate is flipped by
	// WithFormats or WithContent.
	Asserted bool

	// VocabRefined exempts the keyword from the cross-check that every member of
	// a dispatch row declares that row's Vocab. It marks a keyword whose owning
	// row gates on one vocabulary while the keyword itself belongs to another,
	// so its eval step re-checks the finer gate inline.
	VocabRefined bool
}

// Set reports whether the keyword is set on s, by comparing s against an empty
// schema. It saves every caller from holding its own pristine zero value.
func (k *Keyword) Set(s *Schema) bool { return k.Differs(emptySchema, s) }

// valueKeyword fills k's closures over a directly comparable field: the strings
// and bools, plus the pointer-identity sub-schema and const fields. The Differs
// comparison is by value deliberately -- a hook that re-sets a keyword to the
// type's own value is reported as unchanged, dropping a redundant wrapper
// sibling that carries no meaning either way.
func valueKeyword[T comparable](k Keyword, get func(*Schema) T, set func(*Schema, T)) Keyword {
	k.Differs = func(a, b *Schema) bool { return get(a) != get(b) }
	k.Assign = func(src, dst *Schema) { set(dst, get(src)) }

	return k
}

// pointerKeyword fills k's closures over a *T scalar field, comparing the
// pointed-at values (two nils equal, nil versus non-nil unequal) and assigning
// the pointer.
func pointerKeyword[T comparable](k Keyword, get func(*Schema) *T, set func(*Schema, *T)) Keyword {
	k.Differs = func(a, b *Schema) bool { return !ptrEqual(get(a), get(b)) }
	k.Assign = func(src, dst *Schema) { set(dst, get(src)) }

	return k
}

// boundKeyword fills a numeric, length, or count endpoint row: wrapper-scoped,
// intersect-merging, and part of the constraint algebra. T is the pointee type,
// inferred from the accessors, so the *float64 numeric endpoints and the *int
// size endpoints share one constructor.
func boundKeyword[T comparable](
	name, field string,
	get func(*Schema) *T,
	set func(*Schema, *T),
) Keyword {
	// An int-typed endpoint is a length or count, whose value domain the spec
	// fixes as a non-negative integer; a float64-typed one is a numeric bound
	// with the full number domain.
	_, size := any((*T)(nil)).(*int)

	return pointerKeyword(Keyword{
		Fields:   []string{field},
		Name:     name,
		Drafts:   DraftsAll,
		Merge:    MergeIntersect,
		Scope:    ScopeWrapper,
		Vocab:    VocabValidation,
		Bound:    true,
		Size:     size,
		Asserted: true,
	}, get, set)
}

// annotationKeyword fills a wrapper-scoped annotation row over a string or bool
// field: no dispatch row asserts it, so it merges as MergeNone.
func annotationKeyword[T comparable](
	name, field string,
	get func(*Schema) T,
	set func(*Schema, T),
) Keyword {
	return valueKeyword(Keyword{
		Fields: []string{field},
		Name:   name,
		Drafts: DraftsAll,
		Scope:  ScopeWrapper,
	}, get, set)
}

// stringValueKeyword fills a value-scoped string row that replaces the
// type-derived value: pattern, format, and the two content keywords.
func stringValueKeyword(
	name, field string,
	vocab VocabGroup,
	get func(*Schema) string,
	set func(*Schema, string),
) Keyword {
	return valueKeyword(Keyword{
		Fields:   []string{field},
		Name:     name,
		Drafts:   DraftsAll,
		Merge:    MergeReplace,
		Scope:    ScopeValue,
		Vocab:    vocab,
		Asserted: true,
	}, get, set)
}

// structural builds a row for a keyword the field reconciliation never authors
// but a dispatch row asserts: no closures, ScopeNone, MergeNone.
func structural(name string, drafts DraftRange, vocab VocabGroup, fields ...string) Keyword {
	return Keyword{
		Fields:   fields,
		Name:     name,
		Drafts:   drafts,
		Vocab:    vocab,
		Asserted: true,
	}
}

var (
	// DraftsAll matches every draft.
	DraftsAll = DraftRange{DraftMin, DraftMax}

	// Drafts2020Up matches Draft 2020-12 and any newer draft.
	Drafts2020Up = DraftRange{Draft2020, DraftMax}

	// DraftsThrough7 matches Draft-07 and any older draft.
	DraftsThrough7 = DraftRange{DraftMin, Draft7}

	// Keywords is the canonical row per keyword name in internal/keyword. Rows
	// are grouped for reading, not for behavior: the wrapper-scoped annotations,
	// then the wrapper-scoped bound endpoints and the two remaining wrapper rows,
	// then the value-scoped rows, then the structural ones.
	Keywords = []Keyword{
		// Wrapper-scoped annotations: authorable, but no dispatch row asserts
		// them, so moving them onto the null wrapper conjoins nothing.
		annotationKeyword(keyword.Description, "Description",
			func(s *Schema) string { return s.Description },
			func(s *Schema, v string) { s.Description = v }),
		annotationKeyword(keyword.Title, "Title",
			func(s *Schema) string { return s.Title },
			func(s *Schema, v string) { s.Title = v }),
		{
			// Default is json.RawMessage, so it is neither comparable (ruling out
			// valueKeyword) nor a scalar pointer; it compares as its raw bytes.
			Differs: func(a, b *Schema) bool { return string(a.Default) != string(b.Default) },
			Assign:  func(src, dst *Schema) { dst.Default = src.Default },
			Fields:  []string{"Default"},
			Name:    keyword.Default,
			Drafts:  DraftsAll,
			Scope:   ScopeWrapper,
		},
		annotationKeyword(keyword.Deprecated, "Deprecated",
			func(s *Schema) bool { return s.Deprecated },
			func(s *Schema, v bool) { s.Deprecated = v }),
		annotationKeyword(keyword.ReadOnly, "ReadOnly",
			func(s *Schema) bool { return s.ReadOnly },
			func(s *Schema, v bool) { s.ReadOnly = v }),
		annotationKeyword(keyword.WriteOnly, "WriteOnly",
			func(s *Schema) bool { return s.WriteOnly },
			func(s *Schema, v bool) { s.WriteOnly = v }),
		{
			// A field rarely re-authors a type's examples; a length change is a
			// sufficient authored signal (an equal-length re-set is inert either
			// way). This makes the row's presence notion length-based, unlike
			// schemafield's nil-based IsZero for the same field, so the two are
			// not interchangeable.
			Differs: func(a, b *Schema) bool { return len(a.Examples) != len(b.Examples) },
			Assign:  func(src, dst *Schema) { dst.Examples = src.Examples },
			Fields:  []string{"Examples"},
			Name:    keyword.Examples,
			Drafts:  DraftsAll,
			Scope:   ScopeWrapper,
		},
		annotationKeyword(keyword.Comment, "Comment",
			func(s *Schema) string { return s.Comment },
			func(s *Schema, v string) { s.Comment = v }),

		// Wrapper-scoped bound endpoints: the authored value intersects the
		// kind-derived one, so the split's move-and-restore is sound.
		boundKeyword(keyword.Minimum, "Minimum",
			func(s *Schema) *float64 { return s.Minimum },
			func(s *Schema, v *float64) { s.Minimum = v }),
		boundKeyword(keyword.Maximum, "Maximum",
			func(s *Schema) *float64 { return s.Maximum },
			func(s *Schema, v *float64) { s.Maximum = v }),
		boundKeyword(keyword.ExclusiveMinimum, "ExclusiveMinimum",
			func(s *Schema) *float64 { return s.ExclusiveMinimum },
			func(s *Schema, v *float64) { s.ExclusiveMinimum = v }),
		boundKeyword(keyword.ExclusiveMaximum, "ExclusiveMaximum",
			func(s *Schema) *float64 { return s.ExclusiveMaximum },
			func(s *Schema, v *float64) { s.ExclusiveMaximum = v }),
		boundKeyword(keyword.MinLength, "MinLength",
			func(s *Schema) *int { return s.MinLength },
			func(s *Schema, v *int) { s.MinLength = v }),
		boundKeyword(keyword.MaxLength, "MaxLength",
			func(s *Schema) *int { return s.MaxLength },
			func(s *Schema, v *int) { s.MaxLength = v }),
		boundKeyword(keyword.MinItems, "MinItems",
			func(s *Schema) *int { return s.MinItems },
			func(s *Schema, v *int) { s.MinItems = v }),
		boundKeyword(keyword.MaxItems, "MaxItems",
			func(s *Schema) *int { return s.MaxItems },
			func(s *Schema, v *int) { s.MaxItems = v }),
		boundKeyword(keyword.MinProperties, "MinProperties",
			func(s *Schema) *int { return s.MinProperties },
			func(s *Schema, v *int) { s.MinProperties = v }),
		boundKeyword(keyword.MaxProperties, "MaxProperties",
			func(s *Schema) *int { return s.MaxProperties },
			func(s *Schema, v *int) { s.MaxProperties = v }),

		// The remaining wrapper-scoped rows assert, and conjoin soundly, but are
		// not part of the bound algebra.
		valueKeyword(Keyword{
			Fields:   []string{"UniqueItems"},
			Name:     keyword.UniqueItems,
			Drafts:   DraftsAll,
			Merge:    MergeIntersect,
			Scope:    ScopeWrapper,
			Vocab:    VocabValidation,
			Asserted: true,
		},
			func(s *Schema) bool { return s.UniqueItems },
			func(s *Schema, v bool) { s.UniqueItems = v }),
		// Not compares by pointer identity: the split only needs to know a hook
		// swapped the sub-schema in, not whether its contents are equivalent.
		valueKeyword(Keyword{
			Fields:   []string{"Not"},
			Name:     keyword.Not,
			Drafts:   DraftsAll,
			Merge:    MergeIntersect,
			Scope:    ScopeWrapper,
			Vocab:    VocabApplicator,
			Asserted: true,
		},
			func(s *Schema) *Schema { return s.Not },
			func(s, v *Schema) { s.Not = v }),

		// Value-scoped rows: each rides the anyOf's value branch, where null is
		// never subject to it.
		//
		// Const compares by pointer identity, never by dereferencing: a const may
		// hold a slice or a map (both Constraints.SetConst and the tag parser
		// accept one), and comparing two uncomparable dynamic types compiles and
		// then panics at run time.
		valueKeyword(Keyword{
			Fields:   []string{"Const"},
			Name:     keyword.Const,
			Drafts:   DraftsAll,
			Merge:    MergeReplace,
			Scope:    ScopeValue,
			Vocab:    VocabValidation,
			Asserted: true,
		},
			func(s *Schema) *any { return s.Const },
			func(s *Schema, v *any) { s.Const = v }),
		{
			// Enum conjoins soundly, yet an enum on the wrapper would reject the
			// null branch, so it stays value-scoped. Presence is nil-based, not
			// length-based, so a deliberate empty enum still overlays.
			Differs:  func(a, b *Schema) bool { return (a.Enum == nil) != (b.Enum == nil) },
			Assign:   func(src, dst *Schema) { dst.Enum = src.Enum },
			Fields:   []string{"Enum"},
			Name:     keyword.Enum,
			Drafts:   DraftsAll,
			Merge:    MergeIntersect,
			Scope:    ScopeValue,
			Vocab:    VocabValidation,
			Asserted: true,
		},
		stringValueKeyword(keyword.Pattern, "Pattern", VocabValidation,
			func(s *Schema) string { return s.Pattern },
			func(s *Schema, v string) { s.Pattern = v }),
		// Format's assertion is gated by the dispatch table's opt-in field rather
		// than by any optional vocabulary, hence VocabCore.
		stringValueKeyword(keyword.Format, "Format", VocabCore,
			func(s *Schema) string { return s.Format },
			func(s *Schema, v string) { s.Format = v }),
		pointerKeyword(Keyword{
			Fields:   []string{"MultipleOf"},
			Name:     keyword.MultipleOf,
			Drafts:   DraftsAll,
			Merge:    MergeReplace,
			Scope:    ScopeValue,
			Vocab:    VocabValidation,
			Bound:    true,
			Asserted: true,
		},
			func(s *Schema) *float64 { return s.MultipleOf },
			func(s *Schema, v *float64) { s.MultipleOf = v }),
		stringValueKeyword(keyword.ContentEncoding, "ContentEncoding", VocabContent,
			func(s *Schema) string { return s.ContentEncoding },
			func(s *Schema, v string) { s.ContentEncoding = v }),
		stringValueKeyword(keyword.ContentMediaType, "ContentMediaType", VocabContent,
			func(s *Schema) string { return s.ContentMediaType },
			func(s *Schema, v string) { s.ContentMediaType = v }),
		// ContentSchema is authorable and value-scoped like its two siblings, but
		// no dispatch row asserts it: it is an annotation the validator records
		// and never evaluates.
		valueKeyword(Keyword{
			Fields: []string{"ContentSchema"},
			Name:   keyword.ContentSchema,
			Drafts: DraftsAll,
			Scope:  ScopeValue,
		},
			func(s *Schema) *Schema { return s.ContentSchema },
			func(s, v *Schema) { s.ContentSchema = v }),

		// Structural rows: the field reconciliation never authors them, so they
		// carry no closures. The sub-schema-bearing ones are re-rendered from the
		// value node instead of copied.
		structural(keyword.Type, DraftsAll, VocabValidation, "Type", "Types"),
		structural(keyword.Ref, DraftsAll, VocabCore, "Ref"),
		structural(keyword.DynamicRef, Drafts2020Up, VocabCore, "DynamicRef"),
		// The reusable-schema containers assert nothing themselves (their members
		// are reached through $ref), so no dispatch row owns them.
		{Name: keyword.Defs, Fields: []string{"Defs"}, Drafts: DraftsAll},
		{Name: keyword.Definitions, Fields: []string{"Definitions"}, Drafts: DraftsAll},
		structural(keyword.Required, DraftsAll, VocabValidation, "Required"),
		structural(keyword.DependentRequired, Drafts2020Up, VocabValidation, "DependentRequired"),
		structural(keyword.DependentSchemas, Drafts2020Up, VocabApplicator, "DependentSchemas"),
		// The legacy dependencies form spans two Schema fields (the sub-schema map
		// and the required-property map) and, in this implementation, is evaluated
		// under Draft 2020-12 too, so it declares every draft.
		structural(keyword.Dependencies, DraftsAll, VocabCore,
			"DependencySchemas", "DependencyStrings"),
		structural(keyword.PrefixItems, Drafts2020Up, VocabApplicator, "PrefixItems"),
		// Items spans both Schema fields: the single-schema form and the Draft-07
		// array (tuple) form.
		structural(keyword.Items, DraftsAll, VocabApplicator, "Items", "ItemsArray"),
		structural(keyword.AdditionalItems, DraftsThrough7, VocabApplicator, "AdditionalItems"),
		structural(keyword.Contains, DraftsAll, VocabApplicator, "Contains"),
		// The two contains counts belong to the validation vocabulary yet ride the
		// applicator-gated contains row, so they declare the refinement themselves
		// instead of the dispatch table carrying a list of exceptions:
		// evalContains reads them only when the validation vocabulary is also
		// active, and otherwise falls back to the default floor of one match.
		{
			Fields:       []string{"MinContains"},
			Name:         keyword.MinContains,
			Drafts:       Drafts2020Up,
			Vocab:        VocabValidation,
			Size:         true,
			Asserted:     true,
			VocabRefined: true,
		},
		{
			Fields:       []string{"MaxContains"},
			Name:         keyword.MaxContains,
			Drafts:       Drafts2020Up,
			Vocab:        VocabValidation,
			Size:         true,
			Asserted:     true,
			VocabRefined: true,
		},
		structural(keyword.UnevaluatedItems, Drafts2020Up, VocabUnevaluated, "UnevaluatedItems"),
		structural(keyword.Properties, DraftsAll, VocabApplicator, "Properties"),
		structural(keyword.PatternProperties, DraftsAll, VocabApplicator, "PatternProperties"),
		structural(keyword.AdditionalProperties, DraftsAll, VocabApplicator, "AdditionalProperties"),
		structural(keyword.PropertyNames, DraftsAll, VocabApplicator, "PropertyNames"),
		structural(keyword.UnevaluatedProperties, Drafts2020Up, VocabUnevaluated,
			"UnevaluatedProperties"),
		structural(keyword.AllOf, DraftsAll, VocabApplicator, "AllOf"),
		structural(keyword.AnyOf, DraftsAll, VocabApplicator, "AnyOf"),
		structural(keyword.OneOf, DraftsAll, VocabApplicator, "OneOf"),
		structural(keyword.If, DraftsAll, VocabApplicator, "If"),
		structural(keyword.Then, DraftsAll, VocabApplicator, "Then"),
		structural(keyword.Else, DraftsAll, VocabApplicator, "Else"),
	}

	// Movable is the wrapper-scoped, authorable subset: the keywords the nullable
	// split moves onto the anyOf wrapper, restoring the value branch's
	// type-derived value beneath them. The MergeReplace exclusion keeps a
	// replace-semantics keyword out by construction, so nothing has to remember
	// to leave one off.
	Movable = derive(func(k *Keyword) bool {
		return k.Scope == ScopeWrapper && k.Merge != MergeReplace && k.Assign != nil
	})

	// Authored is every keyword the field reconciliation can overlay from the
	// authored canvas. The overlay visits these in declaration order, which is
	// immaterial: each row owns a disjoint set of Schema fields, and the overlay
	// reads no field a row writes.
	Authored = derive(func(k *Keyword) bool { return k.Assign != nil })

	// Bounds is the numeric, length, and count endpoints the shared constraint
	// algebra resolves. It also decides whether an authored canvas carries a
	// bound at all.
	Bounds = derive(func(k *Keyword) bool { return k.Bound })

	// Sizes is the length and count keywords, whose value domain the spec
	// fixes as a non-negative integer. The parent's compile-time domain check
	// pins its keyword list to this set at load.
	Sizes = derive(func(k *Keyword) bool { return k.Size })

	// Asserted is every keyword a dispatch row owns, for the cross-checks against
	// that table.
	Asserted = derive(func(k *Keyword) bool { return k.Asserted })

	// ByName indexes [Keywords] by keyword name, for the cross-checks the parent
	// package runs against its dispatch table at load.
	ByName = func() map[string]*Keyword {
		out := make(map[string]*Keyword, len(Keywords))
		for i := range Keywords {
			out[Keywords[i].Name] = &Keywords[i]
		}

		return out
	}()

	// The emptySchema value is what [Keyword.Set] compares against to test
	// whether a keyword is set; it is never mutated.
	emptySchema = &Schema{}
)

// Names returns the keyword names in set, sorted. The guards that compare a
// derived set against another table's contents all go through it.
func Names(set []*Keyword) []string {
	out := make([]string, 0, len(set))
	for _, k := range set {
		out = append(out, k.Name)
	}

	slices.Sort(out)

	return out
}

// derive filters [Keywords] into a subset of pointers into the table, in
// declaration order. Each derived set states its membership as a predicate over
// the declared columns.
func derive(keep func(*Keyword) bool) []*Keyword {
	var out []*Keyword

	for i := range Keywords {
		if keep(&Keywords[i]) {
			out = append(out, &Keywords[i])
		}
	}

	return out
}

// ptrEqual reports whether two pointers hold equal values, treating two nil
// pointers as equal and a nil and a non-nil as unequal.
func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}
