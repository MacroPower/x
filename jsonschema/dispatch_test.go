package jsonschema_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestAssertionKeywordsCoverage is a maintenance guard over the keyword dispatch
// table, mirroring [jsonschema.SubschemaEntries]'s field-coverage guard for the
// validation walk. It reaches the table's covered set through the public
// [jsonschema.AssertionKeywords] and asserts it is exactly the full set of
// keyword constants minus the pure annotations: every assertion-bearing keyword
// is owned by some dispatch row (no omissions, so a keyword cannot silently lose
// its gate), and no annotation-only keyword is asserted. Duplicate ownership is
// caught separately at package load by the table's init. When a keyword is added
// to the package, allKeywords below must gain it too, which forces the author to
// classify it as an assertion (add it to a row's keywords) or an annotation.
func TestAssertionKeywordsCoverage(t *testing.T) {
	t.Parallel()

	// The annotationKeywords carry no assertion and never affect validity, so
	// the dispatch table deliberately omits them.
	annotationKeywords := []string{
		jsonschema.KeywordTitle,
		jsonschema.KeywordDescription,
		jsonschema.KeywordDefault,
		jsonschema.KeywordExamples,
		jsonschema.KeywordReadOnly,
		jsonschema.KeywordWriteOnly,
		jsonschema.KeywordDeprecated,
		jsonschema.KeywordDefs,
		jsonschema.KeywordDefinitions,
		jsonschema.KeywordContentSchema,
	}

	// The allKeywords slice enumerates every keyword constant the package
	// exports, the mirror of internal/keyword this guard checks the table against.
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

	// A drop here means the enumeration stopped matching the exported constants.
	require.GreaterOrEqual(t, len(allKeywords), 53,
		"allKeywords enumerates fewer constants than the known keyword set")

	excluded := make(map[string]bool, len(annotationKeywords))
	for _, kw := range annotationKeywords {
		require.Contains(t, allKeywords, kw, "annotation keyword %q must appear in allKeywords", kw)

		excluded[kw] = true
	}

	var expected []string

	for _, kw := range allKeywords {
		if !excluded[kw] {
			expected = append(expected, kw)
		}
	}

	slices.Sort(expected)

	assert.Equal(t, expected, jsonschema.AssertionKeywords(),
		"every asserted keyword must be owned by exactly one dispatch row and no annotation keyword may be asserted")
}
