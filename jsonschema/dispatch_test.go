package jsonschema_test

import (
	"encoding/json/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/keywordmeta"
)

// TestAssertionKeywordsCoverage is a maintenance guard over the keyword dispatch
// table, mirroring [jsonschema.SubschemaEntries]'s field-coverage guard for the
// validation walk. It reaches the table's covered set through the public
// [jsonschema.AssertionKeywords] and asserts it is exactly the set of keywords
// the per-keyword semantics table declares asserted: every assertion-bearing
// keyword is owned by some dispatch row (no omissions, so a keyword cannot
// silently lose its gate), and no annotation-only keyword is asserted. Duplicate
// ownership is caught separately at package load by the table's init.
//
// This is a cross-check between two independently maintained tables, declared
// semantics against dispatch, so a keyword misclassified in one shows up here.
func TestAssertionKeywordsCoverage(t *testing.T) {
	t.Parallel()

	assert.Equal(t, keywordmeta.Names(keywordmeta.Asserted), jsonschema.AssertionKeywords(),
		"every asserted keyword must be owned by exactly one dispatch row and no annotation keyword may be asserted")
}

// TestPublicKeywordConstantsMirrorInternal guards the public Keyword* re-exports
// against the internal keyword set they mirror, so a keyword added internally
// without a matching public constant fails here. The one omission is $comment:
// the constants exist for consumers branching on [jsonschema.ValidationError]'s
// Keyword or building schema locations, and $comment carries no assertion, so it
// never appears as either.
func TestPublicKeywordConstantsMirrorInternal(t *testing.T) {
	t.Parallel()

	// The allKeywords slice enumerates every keyword constant the package
	// exports, the mirror of internal/keyword this guard checks.
	allKeywords := []string{
		jsonschema.KeywordAdditionalItems,
		jsonschema.KeywordAdditionalProperties,
		jsonschema.KeywordAllOf,
		jsonschema.KeywordAnyOf,
		jsonschema.KeywordConst,
		jsonschema.KeywordContains,
		jsonschema.KeywordContentEncoding,
		jsonschema.KeywordContentMediaType,
		jsonschema.KeywordContentSchema,
		jsonschema.KeywordDefault,
		jsonschema.KeywordDefinitions,
		jsonschema.KeywordDefs,
		jsonschema.KeywordDependencies,
		jsonschema.KeywordDependentRequired,
		jsonschema.KeywordDependentSchemas,
		jsonschema.KeywordDeprecated,
		jsonschema.KeywordDescription,
		jsonschema.KeywordDynamicRef,
		jsonschema.KeywordElse,
		jsonschema.KeywordEnum,
		jsonschema.KeywordExamples,
		jsonschema.KeywordExclusiveMaximum,
		jsonschema.KeywordExclusiveMinimum,
		jsonschema.KeywordFormat,
		jsonschema.KeywordIf,
		jsonschema.KeywordItems,
		jsonschema.KeywordMaxContains,
		jsonschema.KeywordMaximum,
		jsonschema.KeywordMaxItems,
		jsonschema.KeywordMaxLength,
		jsonschema.KeywordMaxProperties,
		jsonschema.KeywordMinContains,
		jsonschema.KeywordMinimum,
		jsonschema.KeywordMinItems,
		jsonschema.KeywordMinLength,
		jsonschema.KeywordMinProperties,
		jsonschema.KeywordMultipleOf,
		jsonschema.KeywordNot,
		jsonschema.KeywordOneOf,
		jsonschema.KeywordPattern,
		jsonschema.KeywordPatternProperties,
		jsonschema.KeywordPrefixItems,
		jsonschema.KeywordProperties,
		jsonschema.KeywordPropertyNames,
		jsonschema.KeywordReadOnly,
		jsonschema.KeywordRef,
		jsonschema.KeywordRequired,
		jsonschema.KeywordThen,
		jsonschema.KeywordTitle,
		jsonschema.KeywordType,
		jsonschema.KeywordUnevaluatedItems,
		jsonschema.KeywordUnevaluatedProperties,
		jsonschema.KeywordUniqueItems,
		jsonschema.KeywordWriteOnly,
	}

	var internal []string

	for i := range keywordmeta.Keywords {
		if name := keywordmeta.Keywords[i].Name; name != keyword.Comment {
			internal = append(internal, name)
		}
	}

	slices.Sort(internal)
	slices.Sort(allKeywords)

	assert.Equal(t, internal, allKeywords,
		"the public Keyword* constants must mirror the internal keyword set minus $comment")
}

// TestDispatchDraftGatingCoversRows guards the draft-gating table against the
// dispatch table, so a new dispatch row cannot land unprobed. It is the
// counterpart of TestReconcileSplitCoversAuthored, which pins the same kind of
// coverage over the authorable-keyword partition.
func TestDispatchDraftGatingCoversRows(t *testing.T) {
	t.Parallel()

	var covered []string

	for _, tc := range draftGateCases {
		covered = append(covered, tc.keywords...)
	}

	slices.Sort(covered)
	require.Len(t, slices.Compact(slices.Clone(covered)), len(covered),
		"no keyword may be accounted for by two probes")

	assert.Equal(t, jsonschema.AssertionKeywords(), covered,
		"every dispatch row needs a draft-gating probe accounting for its keywords")
}

// TestDraftConstantsInSync pins the parent package's [jsonschema.Draft] enum
// against the copy [keywordmeta] declares. The two are distinct types kept in
// lockstep by value, because the dispatch gate converts one to the other raw and
// [keywordmeta.DraftRange] is a closed interval over the numeric values.
func TestDraftConstantsInSync(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int(jsonschema.Draft7), int(keywordmeta.Draft7),
		"Draft7 must hold the same value in both enums")
	assert.Equal(t, int(jsonschema.Draft2020), int(keywordmeta.Draft2020),
		"Draft2020 must hold the same value in both enums")
}

// draftGateCase is one dispatch row's draft applicability, expressed through the
// public API: a schema whose keyword rejects the instance, and whether that
// rejection happens under each draft.
type draftGateCase struct {
	// The schema and instance are JSON sources: the keyword under probe rejects
	// the instance wherever the row applies.
	schema   string
	instance string
	// The keywords are the dispatch keywords this case's probe accounts for.
	// TestDispatchDraftGatingCoversRows asserts their union is every asserted
	// keyword, which forces a probe for each dispatch row. A second probe of an
	// already-accounted row declares none.
	keywords []string
	// The validate options enable an opt-in assertion so the probed row fires at
	// all (format, content).
	validate []jsonschema.ValidateOption
	// The draft7 and draft2020 flags are whether the rejection happens under
	// each draft.
	draft7    bool
	draft2020 bool
}

// TestDispatchDraftGating pins each dispatch row's draft applicability through
// the public API, with at least one probe per row. It is the behavioral
// baseline for deriving a row's draft range from its member keywords: a
// derivation that narrows a row's range silently stops an assertion firing, and
// one that widens it starts a 2020-12-only keyword firing under Draft-07.
func TestDispatchDraftGating(t *testing.T) {
	t.Parallel()

	for name, tc := range draftGateCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				schema   jsonschema.Schema
				instance any
			)

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))
			require.NoError(t, json.Unmarshal([]byte(tc.instance), &instance))

			fires := func(d jsonschema.Draft) bool {
				opts := append([]jsonschema.ValidateOption{jsonschema.WithDraft(d)}, tc.validate...)

				return jsonschema.Validate(t.Context(), &schema, instance, opts...) != nil
			}

			assert.Equal(t, tc.draft7, fires(jsonschema.Draft7), "Draft-07 applicability")
			assert.Equal(t, tc.draft2020, fires(jsonschema.Draft2020), "Draft 2020-12 applicability")
		})
	}
}

// The draftGateCases table holds at least one probe per dispatch row: a schema
// whose keyword rejects the instance, and whether that rejection happens under
// each draft. Each probe uses the keyword spelling its drafts understand, so a
// row applicable to both fires in both.
var draftGateCases = map[string]draftGateCase{
	"$ref": {
		keywords: []string{jsonschema.KeywordRef},
		schema:   `{"$defs":{"a":{"type":"string"}},"$ref":"#/$defs/a"}`,
		instance: `1`,
		draft7:   true, draft2020: true,
	},
	"$dynamicRef": {
		keywords:  []string{jsonschema.KeywordDynamicRef},
		schema:    `{"$defs":{"a":{"$dynamicAnchor":"T","type":"string"}},"$dynamicRef":"#T"}`,
		instance:  `1`,
		draft2020: true,
	},
	"type": {
		keywords: []string{jsonschema.KeywordType},
		schema:   `{"type":"string"}`, instance: `1`,
		draft7: true, draft2020: true,
	},
	"enum": {
		keywords: []string{jsonschema.KeywordEnum},
		schema:   `{"enum":["a"]}`, instance: `"b"`,
		draft7: true, draft2020: true,
	},
	"const": {
		keywords: []string{jsonschema.KeywordConst},
		schema:   `{"const":"a"}`, instance: `"b"`,
		draft7: true, draft2020: true,
	},
	"numeric": {
		keywords: []string{
			jsonschema.KeywordMultipleOf,
			jsonschema.KeywordMinimum,
			jsonschema.KeywordMaximum,
			jsonschema.KeywordExclusiveMinimum,
			jsonschema.KeywordExclusiveMaximum,
		},
		schema:    `{"minimum":5}`,
		instance:  `1`,
		draft7:    true,
		draft2020: true,
	},
	"string": {
		keywords: []string{jsonschema.KeywordMinLength, jsonschema.KeywordMaxLength, jsonschema.KeywordPattern},
		schema:   `{"minLength":3}`, instance: `"a"`,
		draft7: true, draft2020: true,
	},
	"format": {
		keywords: []string{jsonschema.KeywordFormat},
		schema:   `{"format":"ipv4"}`, instance: `"nope"`,
		validate: []jsonschema.ValidateOption{jsonschema.WithFormats(true)},
		draft7:   true, draft2020: true,
	},
	"array.items": {
		keywords: []string{jsonschema.KeywordPrefixItems, jsonschema.KeywordItems, jsonschema.KeywordAdditionalItems},
		schema:   `{"items":{"type":"string"}}`, instance: `[1]`,
		draft7: true, draft2020: true,
	},
	"contains": {
		keywords: []string{jsonschema.KeywordContains, jsonschema.KeywordMinContains, jsonschema.KeywordMaxContains},
		schema:   `{"contains":{"type":"string"}}`, instance: `[1]`,
		draft7: true, draft2020: true,
	},
	"contains.counts": {
		// The contains row applies to every draft, but evalContains refines
		// the counts inline: Draft-07 has no minContains, so the floor stays
		// at one match.
		schema:    `{"contains":{"type":"string"},"minContains":2}`,
		instance:  `["a",1]`,
		draft2020: true,
	},
	"array.length": {
		keywords: []string{jsonschema.KeywordMinItems, jsonschema.KeywordMaxItems, jsonschema.KeywordUniqueItems},
		schema:   `{"minItems":2}`, instance: `[1]`,
		draft7: true, draft2020: true,
	},
	"object.applicators": {
		keywords: []string{
			jsonschema.KeywordProperties,
			jsonschema.KeywordPatternProperties,
			jsonschema.KeywordAdditionalProperties,
			jsonschema.KeywordPropertyNames,
		},
		schema:    `{"properties":{"a":{"type":"string"}}}`,
		instance:  `{"a":1}`,
		draft7:    true,
		draft2020: true,
	},
	"dependentSchemas": {
		keywords: []string{jsonschema.KeywordDependentSchemas},
		schema:   `{"dependentSchemas":{"a":{"required":["b"]}}}`, instance: `{"a":1}`,
		draft2020: true,
	},
	"object.count": {
		keywords: []string{
			jsonschema.KeywordRequired,
			jsonschema.KeywordMinProperties,
			jsonschema.KeywordMaxProperties,
		},
		schema:    `{"required":["a"]}`,
		instance:  `{}`,
		draft7:    true,
		draft2020: true,
	},
	"dependentRequired": {
		keywords: []string{jsonschema.KeywordDependentRequired},
		schema:   `{"dependentRequired":{"a":["b"]}}`, instance: `{"a":1}`,
		draft2020: true,
	},
	"dependencies.legacy": {
		keywords: []string{jsonschema.KeywordDependencies},
		// This implementation evaluates the legacy form under Draft 2020-12
		// too, which is what the dependencies row declares.
		schema: `{"dependencies":{"a":["b"]}}`, instance: `{"a":1}`,
		draft7: true, draft2020: true,
	},
	"allOf": {
		keywords: []string{jsonschema.KeywordAllOf},
		schema:   `{"allOf":[{"type":"string"}]}`, instance: `1`,
		draft7: true, draft2020: true,
	},
	"anyOf": {
		keywords: []string{jsonschema.KeywordAnyOf},
		schema:   `{"anyOf":[{"type":"string"}]}`, instance: `1`,
		draft7: true, draft2020: true,
	},
	"oneOf": {
		keywords: []string{jsonschema.KeywordOneOf},
		schema:   `{"oneOf":[{"type":"string"}]}`, instance: `1`,
		draft7: true, draft2020: true,
	},
	"not": {
		keywords: []string{jsonschema.KeywordNot},
		schema:   `{"not":{"type":"integer"}}`, instance: `1`,
		draft7: true, draft2020: true,
	},
	"ifThenElse": {
		keywords: []string{jsonschema.KeywordIf, jsonschema.KeywordThen, jsonschema.KeywordElse},
		schema:   `{"if":{"type":"integer"},"then":{"minimum":5}}`, instance: `1`,
		draft7: true, draft2020: true,
	},
	"content": {
		keywords: []string{jsonschema.KeywordContentEncoding, jsonschema.KeywordContentMediaType},
		schema:   `{"contentEncoding":"base64"}`, instance: `"not base64!"`,
		validate: []jsonschema.ValidateOption{jsonschema.WithContent(true)},
		draft7:   true, draft2020: true,
	},
	"unevaluatedProperties": {
		keywords:  []string{jsonschema.KeywordUnevaluatedProperties},
		schema:    `{"properties":{"a":{}},"unevaluatedProperties":false}`,
		instance:  `{"b":1}`,
		draft2020: true,
	},
	"unevaluatedItems": {
		keywords:  []string{jsonschema.KeywordUnevaluatedItems},
		schema:    `{"prefixItems":[{}],"unevaluatedItems":false}`,
		instance:  `[1,2]`,
		draft2020: true,
	},
}
