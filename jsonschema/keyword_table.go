package jsonschema

import (
	"fmt"
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/keywordmeta"
)

// keywordTable is the ordered keyword dispatch table. It drives the Compile-time
// precompute (precomputeRange runs each row's compile step per indexed node) and,
// filtered to the run's applicable rows once at Compile (see [validator.buildActiveRows]),
// the validation walk. Table order is eval order; the phase field partitions the
// rows into ref-resolution, assertion, and unevaluated stages, and the init below
// enforces that partition so an edit cannot float the unevaluated rows -- which
// must observe every other keyword's annotations -- out of the tail.
//
// It is assigned in init rather than as keywordTable's initializer expression to
// sidestep a spurious initialization cycle: the eval funcs the table names
// reference validator.validate, which reads keywordTable, and Go's
// variable-initializer cycle detection flags that chain even though it never
// forms a runtime cycle. An assignment inside init is a statement, not a
// variable initializer, so it is exempt.
var keywordTable []keywordEntry

// init builds the dispatch table, derives each row's draft range from its member
// keywords, and enforces the table's invariants at package load, turning the
// "unevaluated must run last" rule from a convention an edit could break into a
// checked fact: phases are non-decreasing down the table (so the
// phaseUnevaluated rows form a contiguous tail), and every unevaluated row
// carries the unevaluated vocabulary and the 2020-12-and-up draft range. A
// violation panics at load rather than silently mis-ordering annotation
// evaluation.
func init() {
	keywordTable = []keywordEntry{
		// Ref-resolution phase (phaseRef) runs first.
		{
			name:     "$ref",
			keywords: []string{KeywordRef},
			vocab:    vocabCore,
			phase:    phaseRef,
			isRef:    true,
			eval:     evalRef,
		},
		{
			name:     "$dynamicRef",
			keywords: []string{KeywordDynamicRef},
			vocab:    vocabCore,
			phase:    phaseRef,
			eval:     evalDynamicRef,
		},

		// Assertion phase (phaseAssert): the assertion and applicator keywords.
		{
			name:     "type",
			keywords: []string{KeywordType},
			vocab:    vocabValidation,
			phase:    phaseAssert,
			eval:     evalType,
		},
		{
			name:     "enum",
			keywords: []string{KeywordEnum},
			vocab:    vocabValidation,
			phase:    phaseAssert,
			compile:  enumCompile,
			eval:     evalEnum,
		},
		{
			name:     "const",
			keywords: []string{KeywordConst},
			vocab:    vocabValidation,
			phase:    phaseAssert,
			compile:  constCompile,
			eval:     evalConst,
		},
		{
			name: "numeric",
			keywords: []string{
				KeywordMultipleOf, KeywordMinimum, KeywordMaximum,
				KeywordExclusiveMinimum, KeywordExclusiveMaximum,
			},
			vocab:   vocabValidation,
			phase:   phaseAssert,
			compile: numericCompile,
			eval:    evalNumeric,
		},
		{
			name:     "string",
			keywords: []string{KeywordMinLength, KeywordMaxLength, KeywordPattern},
			vocab:    vocabValidation,
			phase:    phaseAssert,
			compile:  stringCompile,
			eval:     evalString,
		},
		{
			name:     "format",
			keywords: []string{KeywordFormat},
			vocab:    vocabCore,
			optIn:    optInFormat,
			phase:    phaseAssert,
			eval:     evalFormat,
		},
		{
			name:     "array.items",
			keywords: []string{KeywordPrefixItems, KeywordItems, KeywordAdditionalItems},
			vocab:    vocabApplicator,
			phase:    phaseAssert,
			compile:  itemsCompile,
			eval:     evalArrayItems,
		},
		{
			name:     "contains",
			keywords: []string{KeywordContains, KeywordMinContains, KeywordMaxContains},
			vocab:    vocabApplicator,
			phase:    phaseAssert,
			eval:     evalContains,
		},
		{
			name:     "array.length",
			keywords: []string{KeywordMinItems, KeywordMaxItems, KeywordUniqueItems},
			vocab:    vocabValidation,
			phase:    phaseAssert,
			eval:     evalArrayLength,
		},
		{
			name: "object.applicators",
			keywords: []string{
				KeywordProperties, KeywordPatternProperties,
				KeywordAdditionalProperties, KeywordPropertyNames,
			},
			vocab:   vocabApplicator,
			phase:   phaseAssert,
			compile: objectApplicatorsCompile,
			eval:    evalObjectApplicators,
		},
		{
			name:     "dependentSchemas",
			keywords: []string{KeywordDependentSchemas},
			vocab:    vocabApplicator,
			phase:    phaseAssert,
			eval:     evalDependentSchemas,
		},
		{
			name:     "object.count",
			keywords: []string{KeywordRequired, KeywordMinProperties, KeywordMaxProperties},
			vocab:    vocabValidation,
			phase:    phaseAssert,
			eval:     evalObjectCount,
		},
		{
			name:     "dependentRequired",
			keywords: []string{KeywordDependentRequired},
			vocab:    vocabValidation,
			phase:    phaseAssert,
			eval:     evalDependentRequired,
		},
		{
			name:     "dependencies.legacy",
			keywords: []string{KeywordDependencies},
			vocab:    vocabCore,
			phase:    phaseAssert,
			eval:     evalLegacyDependencies,
		},
		{
			name:     "allOf",
			keywords: []string{KeywordAllOf},
			vocab:    vocabApplicator,
			phase:    phaseAssert,
			eval:     evalAllOf,
		},
		{
			name:     "anyOf",
			keywords: []string{KeywordAnyOf},
			vocab:    vocabApplicator,
			phase:    phaseAssert,
			eval:     evalAnyOf,
		},
		{
			name:     "oneOf",
			keywords: []string{KeywordOneOf},
			vocab:    vocabApplicator,
			phase:    phaseAssert,
			eval:     evalOneOf,
		},
		{
			name:     "not",
			keywords: []string{KeywordNot},
			vocab:    vocabApplicator,
			phase:    phaseAssert,
			eval:     evalNot,
		},
		{
			name:     "ifThenElse",
			keywords: []string{KeywordIf, KeywordThen, KeywordElse},
			vocab:    vocabApplicator,
			phase:    phaseAssert,
			eval:     evalIfThenElse,
		},
		{
			name:     "content",
			keywords: []string{KeywordContentEncoding, KeywordContentMediaType},
			vocab:    vocabContent,
			optIn:    optInContent,
			phase:    phaseAssert,
			eval:     evalContent,
		},

		// Unevaluated phase (phaseUnevaluated): must run after every applicator
		// above so the annotation state they record is fully merged.
		{
			name:     "unevaluatedProperties",
			keywords: []string{KeywordUnevaluatedProperties},
			vocab:    vocabUnevaluated,
			phase:    phaseUnevaluated,
			eval:     evalUnevaluatedProperties,
		},
		{
			name:     "unevaluatedItems",
			keywords: []string{KeywordUnevaluatedItems},
			vocab:    vocabUnevaluated,
			phase:    phaseUnevaluated,
			eval:     evalUnevaluatedItems,
		},
	}

	deriveRowDrafts()
	checkRowVocabularies()

	for i := 1; i < len(keywordTable); i++ {
		if keywordTable[i].phase < keywordTable[i-1].phase {
			panic(fmt.Sprintf(
				"jsonschema: keywordTable phase order broken: %q (phase %d) follows %q (phase %d)",
				keywordTable[i].name, keywordTable[i].phase,
				keywordTable[i-1].name, keywordTable[i-1].phase,
			))
		}
	}

	for i := range keywordTable {
		e := &keywordTable[i]
		if e.phase != phaseUnevaluated {
			continue
		}

		if e.vocab != vocabUnevaluated {
			panic(fmt.Sprintf("jsonschema: unevaluated row %q must carry vocabUnevaluated", e.name))
		}

		// The range is derived rather than authored, so this asserts that the
		// hull of the row's member keywords came out 2020-12-and-up.
		if e.drafts != keywordmeta.Drafts2020Up {
			panic(fmt.Sprintf("jsonschema: unevaluated row %q must derive draft2020Up", e.name))
		}
	}

	// No keyword may be owned by two rows: the dispatch loop would evaluate it
	// twice, and the coverage guard ([AssertionKeywords]) dedups its result so it
	// cannot see the duplication. Catch it here at load instead.
	owner := map[string]string{}

	for i := range keywordTable {
		e := &keywordTable[i]
		for _, kw := range e.keywords {
			if prev, dup := owner[kw]; dup {
				panic(fmt.Sprintf("jsonschema: keyword %q owned by both %q and %q", kw, prev, e.name))
			}

			owner[kw] = e.name
		}
	}
}

// deriveRowDrafts sets each dispatch row's draft range to the hull of its member
// keywords' declared ranges, so a keyword's applicability is stated once in
// [keywordmeta.Keywords] rather than twice. It runs before the table's other
// invariant checks, which read the derived value.
//
// A hull can only widen, so it could admit a draft no member declares: a row
// spanning {Draft-07 and older} and {2020-12 and newer} would derive "every
// draft" while covering neither the gap between them nor a future draft added
// inside it. Requiring some member to declare the hull outright rules that out,
// since that member covers the whole derived range and no gap can exist within
// it. Sampling the drafts the enum names would not, because it cannot speak for
// a draft added later.
func deriveRowDrafts() {
	for i := range keywordTable {
		e := &keywordTable[i]

		if len(e.keywords) == 0 {
			panic(fmt.Sprintf("jsonschema: dispatch row %q owns no keywords", e.name))
		}

		hull := rowMeta(e, e.keywords[0]).Drafts
		for _, kw := range e.keywords[1:] {
			hull = hull.Hull(rowMeta(e, kw).Drafts)
		}

		spansHull := func(kw string) bool { return rowMeta(e, kw).Drafts == hull }
		if !slices.ContainsFunc(e.keywords, spansHull) {
			panic(fmt.Sprintf(
				"jsonschema: dispatch row %q derives a draft range no member keyword declares outright, "+
					"so it may admit a draft none of them covers",
				e.name,
			))
		}

		e.drafts = hull
	}
}

// checkRowVocabularies asserts each dispatch row's hand-written vocabulary
// against its member keywords' declared ones. The vocabulary stays hand-written
// on the row (a row gates as a unit, and a flat enum has no hull to derive) and
// is cross-checked instead: every member must declare the row's group, unless
// the member declares [keywordmeta.Keyword.VocabRefined], meaning its eval step
// re-checks a finer gate inline.
func checkRowVocabularies() {
	for i := range keywordTable {
		e := &keywordTable[i]

		for _, kw := range e.keywords {
			meta := rowMeta(e, kw)
			if meta.VocabRefined {
				continue
			}

			if meta.Vocab != e.vocab {
				panic(fmt.Sprintf(
					"jsonschema: dispatch row %q carries vocabulary %d but its keyword %q declares %d",
					e.name, e.vocab, kw, meta.Vocab,
				))
			}
		}
	}
}

// rowMeta returns the declared semantics for one of row e's member keywords,
// panicking when the dispatch table names a keyword the semantics table does not
// declare.
func rowMeta(e *keywordEntry, kw string) *keywordmeta.Keyword {
	meta, ok := keywordmeta.ByName[kw]
	if !ok {
		panic(fmt.Sprintf("jsonschema: dispatch row %q owns undeclared keyword %q", e.name, kw))
	}

	return meta
}

// itemsPlan is the Compile-time normalization of a schema's array item keywords
// into a draft-independent shape, so the array.items eval iterates it with no
// per-node draft branch. It resolves the two draft spellings of tuple items
// (Draft 2020-12 prefixItems, Draft-07 array-form items) and of trailing items
// (2020-12 single-schema items, Draft-07 additionalItems) once.
//
// Field order is tuned for struct packing (govet fieldalignment); the grouping
// is not semantic.
type itemsPlan struct {
	// The tupleLabel names the tuple keyword (prefixItems or items) for error paths.
	tupleLabel string
	// The restLabel names the rest keyword (items or additionalItems) for error paths.
	restLabel string
	// The rest subschema applies to indexes at or beyond len(tuple): the 2020-12
	// single-schema items or Draft-07 additionalItems.
	rest *Schema
	// The tuple holds the per-index tuple subschemas: prefixItems or the Draft-07
	// items array.
	tuple []*Schema
	// The restMarksAllItems flag captures the SetAllItems annotation policy
	// declaratively: the 2020-12 items rest marks every trailing item evaluated
	// (so unevaluatedItems does not re-fire on them), while Draft-07
	// additionalItems records no watermark. It is true exactly when rest is an
	// items keyword.
	restMarksAllItems bool
}

// computeItemsPlan builds the [itemsPlan] for a schema under the run's draft,
// or nil when the schema sets no array item keywords. It selects, per draft, the
// tuple and rest subschemas: under Draft 2020-12 prefixItems are the tuple and
// items is the rest; under Draft-07 the array-form items is the tuple with
// additionalItems as the rest, or a single-schema items applies to every index.
func computeItemsPlan(v *validator, s *Schema) *itemsPlan {
	var p itemsPlan

	if v.profile.prefixItemsTuple {
		if len(s.PrefixItems) > 0 {
			p.tuple = s.PrefixItems
			p.tupleLabel = KeywordPrefixItems
		}

		if s.Items != nil {
			p.rest = s.Items
			p.restLabel = KeywordItems
			p.restMarksAllItems = true
		}
	} else {
		// Draft-07: the array form of items spells a tuple, with additionalItems
		// governing the trailing elements. PrefixItems is not a Draft-07 keyword
		// and is ignored. The nil check (not a length check) keeps a
		// present-but-empty items array (JSON "items": []) in the tuple branch:
		// additionalItems then applies from index zero, rather than being
		// silently dropped by falling through to the single-schema case, whose
		// Items is nil when ItemsArray absorbed the keyword.
		switch {
		case s.ItemsArray != nil:
			p.tuple = s.ItemsArray
			p.tupleLabel = KeywordItems

			if s.AdditionalItems != nil {
				p.rest = s.AdditionalItems
				p.restLabel = KeywordAdditionalItems
			}

		case s.Items != nil:
			p.rest = s.Items
			p.restLabel = KeywordItems
			p.restMarksAllItems = true
		}
	}

	if p.tuple == nil && p.rest == nil {
		return nil
	}

	return &p
}

// itemsCompile records a schema's [itemsPlan] in the Compile-time cache when the
// schema sets any array item keyword, so the array.items eval avoids rebuilding
// it per node. Schemas with no item keywords are left absent; itemsPlanFor
// returns nil for them.
func itemsCompile(v *validator, id int, s *Schema) {
	if p := computeItemsPlan(v, s); p != nil {
		v.itemsPlans[id] = p
	}
}

// itemsPlanFor returns the array item plan for schema, preferring the per-node
// cache and computing on the fly for a schema outside the index (a remote
// or JSON-pointer fallback schema reached only at validation time), mirroring
// [validator.boundsFor]. It returns nil when the schema sets no array item
// keyword.
func (v *validator) itemsPlanFor(id int, schema *Schema) *itemsPlan {
	if v.inIndex(id) && v.itemsPlans[id] != nil {
		return v.itemsPlans[id]
	}

	return computeItemsPlan(v, schema)
}
