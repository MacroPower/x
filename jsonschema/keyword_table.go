package jsonschema

import "fmt"

// keywordTable is the ordered keyword dispatch table. It drives the Compile-time
// precompute (precomputeSchema runs each row's compile step) and, filtered to
// the run's applicable rows once at Compile (see [validator.buildActiveRows]),
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

// init builds the dispatch table and enforces its ordering invariants at package
// load, turning the "unevaluated must run last" rule from a convention an edit
// could break into a checked fact: phases are non-decreasing down the table (so
// the phaseUnevaluated rows form a contiguous tail), and every unevaluated row
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
			drafts:   draftAll,
			phase:    phaseRef,
			isRef:    true,
			eval:     evalRef,
		},
		{
			name:     "$dynamicRef",
			keywords: []string{KeywordDynamicRef},
			vocab:    vocabCore,
			drafts:   draft2020Up,
			phase:    phaseRef,
			eval:     evalDynamicRef,
		},

		// Assertion phase (phaseAssert): the assertion and applicator keywords.
		{
			name:     "type",
			keywords: []string{KeywordType},
			vocab:    vocabValidation,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalType,
		},
		{
			name:     "enum",
			keywords: []string{KeywordEnum},
			vocab:    vocabValidation,
			drafts:   draftAll,
			phase:    phaseAssert,
			compile:  enumCompile,
			eval:     evalEnum,
		},
		{
			name:     "const",
			keywords: []string{KeywordConst},
			vocab:    vocabValidation,
			drafts:   draftAll,
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
			drafts:  draftAll,
			phase:   phaseAssert,
			compile: numericCompile,
			eval:    evalNumeric,
		},
		{
			name:     "string",
			keywords: []string{KeywordMinLength, KeywordMaxLength, KeywordPattern},
			vocab:    vocabValidation,
			drafts:   draftAll,
			phase:    phaseAssert,
			compile:  stringCompile,
			eval:     evalString,
		},
		{
			name:     "format",
			keywords: []string{KeywordFormat},
			vocab:    vocabCore,
			optIn:    optInFormat,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalFormat,
		},
		{
			name:     "array.items",
			keywords: []string{KeywordPrefixItems, KeywordItems, KeywordAdditionalItems},
			vocab:    vocabApplicator,
			drafts:   draftAll,
			phase:    phaseAssert,
			compile:  itemsCompile,
			eval:     evalArrayItems,
		},
		{
			name:     "contains",
			keywords: []string{KeywordContains, KeywordMinContains, KeywordMaxContains},
			vocab:    vocabApplicator,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalContains,
		},
		{
			name:     "array.length",
			keywords: []string{KeywordMinItems, KeywordMaxItems, KeywordUniqueItems},
			vocab:    vocabValidation,
			drafts:   draftAll,
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
			drafts:  draftAll,
			phase:   phaseAssert,
			compile: objectApplicatorsCompile,
			eval:    evalObjectApplicators,
		},
		{
			name:     "dependentSchemas",
			keywords: []string{KeywordDependentSchemas},
			vocab:    vocabApplicator,
			drafts:   draft2020Up,
			phase:    phaseAssert,
			eval:     evalDependentSchemas,
		},
		{
			name:     "object.count",
			keywords: []string{KeywordRequired, KeywordMinProperties, KeywordMaxProperties},
			vocab:    vocabValidation,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalObjectCount,
		},
		{
			name:     "dependentRequired",
			keywords: []string{KeywordDependentRequired},
			vocab:    vocabValidation,
			drafts:   draft2020Up,
			phase:    phaseAssert,
			eval:     evalDependentRequired,
		},
		{
			name:     "dependencies.legacy",
			keywords: []string{KeywordDependencies},
			vocab:    vocabCore,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalLegacyDependencies,
		},
		{
			name:     "allOf",
			keywords: []string{KeywordAllOf},
			vocab:    vocabApplicator,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalAllOf,
		},
		{
			name:     "anyOf",
			keywords: []string{KeywordAnyOf},
			vocab:    vocabApplicator,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalAnyOf,
		},
		{
			name:     "oneOf",
			keywords: []string{KeywordOneOf},
			vocab:    vocabApplicator,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalOneOf,
		},
		{
			name:     "not",
			keywords: []string{KeywordNot},
			vocab:    vocabApplicator,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalNot,
		},
		{
			name:     "ifThenElse",
			keywords: []string{KeywordIf, KeywordThen, KeywordElse},
			vocab:    vocabApplicator,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalIfThenElse,
		},
		{
			name:     "content",
			keywords: []string{KeywordContentEncoding, KeywordContentMediaType},
			vocab:    vocabContent,
			optIn:    optInContent,
			drafts:   draftAll,
			phase:    phaseAssert,
			eval:     evalContent,
		},

		// Unevaluated phase (phaseUnevaluated): must run after every applicator
		// above so the annotation state they record is fully merged.
		{
			name:     "unevaluatedProperties",
			keywords: []string{KeywordUnevaluatedProperties},
			vocab:    vocabUnevaluated,
			drafts:   draft2020Up,
			phase:    phaseUnevaluated,
			eval:     evalUnevaluatedProperties,
		},
		{
			name:     "unevaluatedItems",
			keywords: []string{KeywordUnevaluatedItems},
			vocab:    vocabUnevaluated,
			drafts:   draft2020Up,
			phase:    phaseUnevaluated,
			eval:     evalUnevaluatedItems,
		},
	}

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

		if e.drafts != draft2020Up {
			panic(fmt.Sprintf("jsonschema: unevaluated row %q must carry draft2020Up", e.name))
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

	if v.draft == Draft2020 {
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
func itemsCompile(v *validator, s *Schema) {
	if p := computeItemsPlan(v, s); p != nil {
		v.itemsPlans[s] = p
	}
}

// itemsPlanFor returns the array item plan for schema, preferring the
// Compile-time cache and computing on the fly for a schema absent from it (a
// remote or JSON-pointer fallback schema reached only at validation time),
// mirroring [validator.boundsFor]. It returns nil when the schema sets no array
// item keyword.
func (v *validator) itemsPlanFor(schema *Schema) *itemsPlan {
	if p, ok := v.itemsPlans[schema]; ok {
		return p
	}

	return computeItemsPlan(v, schema)
}
