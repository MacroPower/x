package jsonschema

import (
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/annotations"
	"go.jacobcolvin.com/x/jsonschema/internal/keywordmeta"
)

// vocabGroup names the vocabulary that owns a keyword row, so the dispatch loop
// can decide a row's applicability from the resolved [vocab.Set] in one place
// instead of each keyword branch re-checking it inline. The vocabCore group is
// always active: it covers the keywords ($ref, $dynamicRef, format, the legacy
// dependencies form) that are not part of any optional 2020-12 vocabulary and
// must apply regardless of which vocabularies a metaschema activates.
//
// The type aliases [keywordmeta.VocabGroup] and the constants alias its
// constants, where each individual keyword declares the same grouping; the init
// in keyword_table.go cross-checks each row against its members.
type vocabGroup = keywordmeta.VocabGroup

const (
	vocabCore        = keywordmeta.VocabCore        // always active ($ref, $dynamicRef, format, legacy dependencies)
	vocabApplicator  = keywordmeta.VocabApplicator  // vocab.Set.Applicator
	vocabValidation  = keywordmeta.VocabValidation  // vocab.Set.Validation
	vocabUnevaluated = keywordmeta.VocabUnevaluated // vocab.Set.Unevaluated
	vocabContent     = keywordmeta.VocabContent     // vocab.Set.Content
)

// optIn is an orthogonal gate for keywords whose assertion is opt-in beyond
// vocabulary membership. The format and content keywords default to
// annotation-only and are asserted only when the run enabled them (see
// [WithFormats] and [WithContent], resolved into the validator's
// formatsEnabled/contentEnabled bools at Compile time). Folding that state into
// a single declarative gate keeps the option check out of each eval step.
type optIn uint8

const (
	optInNone optIn = iota
	optInFormat
	optInContent
)

// draftRange expresses a keyword's applicability as a closed interval over the
// ordered [Draft] values: an O(1), allocation-free membership test that is
// monotonic with draft removal (a keyword introduced at one draft and removed
// at another names the interval between them). Its sentinels are spaced far
// beyond the current Draft values (Draft7=-100, Draft2020=0) so an open-ended
// range genuinely means "and any future newer draft" rather than collapsing to
// the single point {Draft2020}.
//
// The type aliases [keywordmeta.DraftRange], where each individual keyword
// declares its own range; a dispatch row's range is derived from its members in
// keyword_table.go's init rather than written here.
type draftRange = keywordmeta.DraftRange

// phase groups keyword rows into ordered evaluation stages. Table order is eval
// order, but phase makes the one ordering guarantee that matters -- the
// unevaluated* keywords must run after every other applicator so annotations
// are fully merged -- a checked invariant (see the init in keyword_table.go)
// rather than a convention an edit could silently break.
type phase uint8

const (
	phaseRef phase = iota
	phaseAssert
	phaseUnevaluated
)

// keywordEntry is one row of the keyword dispatch table: a keyword group
// represented as data (owning vocabulary, derived draft range, opt-in gate, an
// optional compile-time precompute step, and an eval step) so draft and
// vocabulary gating is a declarative fact checked once in the dispatch loop
// rather than a conditional remembered at each keyword branch.
//
// Field order is tuned for struct packing (govet fieldalignment); the grouping
// is not semantic.
type keywordEntry struct {
	// The compile step runs once per schema at Compile time to populate the
	// read-only per-node caches this row's eval consults, storing under the
	// schema's node id; nil when the row precomputes nothing.
	compile func(v *validator, id int, s *Schema)
	// The eval step asserts this row's keywords against one instance node,
	// returning the errors it found (empty when the node satisfies them).
	eval func(ctx evalContext) []*ValidationError
	// The name is a human-readable group label for introspection and the
	// ordering invariant's panic messages; it does not affect dispatch.
	name string
	// The keywords are the keyword.X constants this row owns. This drives the
	// coverage guard (see [AssertionKeywords]): most rows own several constants,
	// so flattening this field across the table yields the set of keywords the
	// validator asserts, and a keyword added to the constants but not to any
	// row's keywords is caught automatically.
	keywords []string
	// The drafts range is the only field the table literal leaves unset:
	// deriveRowDrafts fills it at load from the row's member keywords, so a
	// keyword's applicability is declared once in [keywordmeta.Keywords].
	drafts draftRange
	vocab  vocabGroup
	optIn  optIn
	phase  phase
	// The isRef flag marks the $ref row so the dispatch loop can implement
	// Draft-07 $ref sibling suppression (a $ref with siblings ignores the
	// siblings).
	isRef bool
}

// evalContext bundles the state every eval step needs for one instance node. It
// is passed by value: threading a *evalContext through the indirect (func
// pointer) eval calls would defeat escape analysis and heap-allocate the
// context once per node on the recursive walk, a real regression. A by-value
// copy of this small struct is stack/register-cheap and allocation-free. No
// eval mutates ctx for a later row (each row's per-node scratch stays a
// function local, and ann is a pointer whose recorded annotations already
// persist), so eval returns only its error slice.
//
// Field order is tuned for struct packing (govet fieldalignment); the grouping
// is not semantic.
type evalContext struct {
	v            *validator
	schema       *Schema
	ann          *annotations.Set
	instance     any
	instancePath instanceLocation
	schemaPath   schemaLocation
	// The nodeID is schema's node id, or -1 when schema is outside the
	// index (a fallback target reached only at validation time). The
	// per-node cache accessors index their slices by it and recompute on -1. It
	// trails the pointer-bearing fields so it does not extend the GC scan prefix
	// (govet fieldalignment).
	nodeID int
}

// vocabActive reports whether the vocabulary group g is active for this run.
// Core is always active; the rest map to the resolved [vocab.Set] bools. Under
// Draft-07, resolveVocabularies sets the full set, so every group is active.
func (v *validator) vocabActive(g vocabGroup) bool {
	switch g {
	case vocabCore:
		return true
	case vocabApplicator:
		return v.vocabs.Applicator
	case vocabValidation:
		return v.vocabs.Validation
	case vocabUnevaluated:
		return v.vocabs.Unevaluated
	case vocabContent:
		return v.vocabs.Content
	}

	return false
}

// gatePasses reports whether entry e applies to this run: its draft interval
// covers the resolved draft, its owning vocabulary is active, and its opt-in
// gate (if any) is enabled. This is the single site where draft and vocabulary
// applicability is decided, rather than a conditional at each keyword branch.
func (v *validator) gatePasses(e *keywordEntry) bool {
	if !e.drafts.Contains(keywordmeta.Draft(v.draft)) {
		return false
	}

	if !v.vocabActive(e.vocab) {
		return false
	}

	switch e.optIn {
	case optInFormat:
		return v.formatsEnabled
	case optInContent:
		return v.contentEnabled
	case optInNone:
		return true
	}

	return true
}

// buildActiveRows records the keywordTable rows this run evaluates -- those
// gatePasses accepts -- in table (eval) order. Because the gate depends only on
// run-fixed state, this runs once at Compile; the per-node walk then iterates
// [validator.activeRows] directly, skipping the gate checks entirely and never
// visiting rows that a draft or disabled vocabulary rules out (e.g. the 2020-12
// rows under Draft-07, or format/content when their assertion is off).
func (v *validator) buildActiveRows() {
	rows := make([]*keywordEntry, 0, len(keywordTable))

	for i := range keywordTable {
		if v.gatePasses(&keywordTable[i]) {
			rows = append(rows, &keywordTable[i])
		}
	}

	v.activeRows = rows
}

// AssertionKeywords returns, sorted, the JSON Schema keyword names the validator
// evaluates as assertions. Pure annotation keywords (title, description,
// default, examples, readOnly, writeOnly, deprecated, definitions, $defs,
// contentSchema) are omitted: they carry no assertion and never affect
// validity. The set is fixed at build time from the validator's keyword
// dispatch table, so it is the authoritative answer to "which keywords does
// this validator act on" and the basis of the table's coverage guard.
func AssertionKeywords() []string {
	var names []string

	for i := range keywordTable {
		names = append(names, keywordTable[i].keywords...)
	}

	slices.Sort(names)

	return slices.Compact(names)
}
