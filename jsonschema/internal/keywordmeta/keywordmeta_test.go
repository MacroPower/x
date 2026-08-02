package keywordmeta //nolint:testpackage // In-package by design: the canonical keyword table is guarded from inside its own package (see jsonschema/CLAUDE.md); the no-in-package-test policy is main-package only.

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
)

var (
	// The allKeywordConstants list enumerates every name in internal/keyword. Go
	// cannot reflect over constants, so this is a hand-list: it catches a row that
	// names something no constant defines, and a constant no row covers, but it
	// cannot catch a 56th constant added upstream of the list itself.
	// TestKeywordMetaCoversSchemaFields catches that, by way of the Schema field a
	// new keyword arrives with.
	allKeywordConstants = []string{
		keyword.AdditionalItems,
		keyword.AdditionalProperties,
		keyword.AllOf,
		keyword.AnyOf,
		keyword.Comment,
		keyword.Const,
		keyword.Contains,
		keyword.ContentEncoding,
		keyword.ContentMediaType,
		keyword.ContentSchema,
		keyword.Default,
		keyword.Definitions,
		keyword.Defs,
		keyword.Dependencies,
		keyword.DependentRequired,
		keyword.DependentSchemas,
		keyword.Deprecated,
		keyword.Description,
		keyword.DynamicRef,
		keyword.Else,
		keyword.Enum,
		keyword.Examples,
		keyword.ExclusiveMaximum,
		keyword.ExclusiveMinimum,
		keyword.Format,
		keyword.If,
		keyword.Items,
		keyword.MaxContains,
		keyword.Maximum,
		keyword.MaxItems,
		keyword.MaxLength,
		keyword.MaxProperties,
		keyword.MinContains,
		keyword.Minimum,
		keyword.MinItems,
		keyword.MinLength,
		keyword.MinProperties,
		keyword.MultipleOf,
		keyword.Not,
		keyword.OneOf,
		keyword.Pattern,
		keyword.PatternProperties,
		keyword.PrefixItems,
		keyword.Properties,
		keyword.PropertyNames,
		keyword.ReadOnly,
		keyword.Ref,
		keyword.Required,
		keyword.Then,
		keyword.Title,
		keyword.Type,
		keyword.UnevaluatedItems,
		keyword.UnevaluatedProperties,
		keyword.UniqueItems,
		keyword.WriteOnly,
	}

	// The unclaimedFields map holds the [schemafield.Fields] rows no keyword row
	// claims, each with the reason it has no keyword semantics to declare.
	unclaimedFields = map[string]string{
		"ID":            "identifier with no keyword constant: never authored, never dispatched",
		"Schema":        "identifier with no keyword constant: never authored, never dispatched",
		"Anchor":        "identifier with no keyword constant: never authored, never dispatched",
		"DynamicAnchor": "identifier with no keyword constant: never authored, never dispatched",
		"Vocabulary":    "identifier with no keyword constant: never authored, never dispatched",
		"PropertyOrder": "render-only: affects JSON output order, never validation",
		"Extra":         "extension catch-all for unknown keywords, which have no declared semantics",
	}
)

// TestKeywordMetaCoversSchemaFields is the staleness guard, in the spirit of
// schemafield's TestFieldTableMatchesUpstream: every exported [Schema] field is
// claimed by exactly one keyword row or allowlisted with a reason. It chains
// through to upstream, since TestFieldTableMatchesUpstream fails on an
// unclassified upstream addition. That makes it the forcing function for a new
// keyword: one essentially always arrives with a new Schema field, so it cannot
// be added upstream without a semantics row landing here.
func TestKeywordMetaCoversSchemaFields(t *testing.T) {
	t.Parallel()

	claimedBy := map[string]string{}

	for i := range Keywords {
		k := &Keywords[i]

		require.NotEmpty(t, k.Fields, "keyword %q must claim at least one Schema field", k.Name)

		for _, field := range k.Fields {
			prev, dup := claimedBy[field]
			require.False(t, dup,
				"Schema field %q claimed by both %q and %q", field, prev, k.Name)

			claimedBy[field] = k.Name
		}
	}

	for i := range schemafield.Fields {
		name := schemafield.Fields[i].Name

		if reason, allowed := unclaimedFields[name]; allowed {
			assert.NotContains(t, claimedBy, name,
				"Schema field %q is allowlisted (%s) but a keyword row claims it", name, reason)

			continue
		}

		assert.Contains(t, claimedBy, name,
			"Schema field %q is claimed by no keyword row: add a row, or allowlist it with a reason", name)
	}

	known := make(map[string]bool, len(schemafield.Fields))
	for i := range schemafield.Fields {
		known[schemafield.Fields[i].Name] = true
	}

	for field, owner := range claimedBy {
		assert.True(t, known[field],
			"keyword %q claims %q, which is not a schemafield row", owner, field)
	}
}

// TestKeywordMetaColumnsConsistent asserts the load-time invariants that hold
// between a row's columns. The ScopeWrapper-implies-not-MergeReplace check is
// the one that would have caught both prior bugs (multipleOf, then pattern and
// format, each landing in the movable set where the split conjoined the authored
// value with the type-derived one it restored).
func TestKeywordMetaColumnsConsistent(t *testing.T) {
	t.Parallel()

	for i := range Keywords {
		k := &Keywords[i]

		t.Run(k.Name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, k.Differs == nil, k.Assign == nil,
				"Differs and Assign must be declared together: a keyword cannot be authorable without being comparable")

			if k.Scope == ScopeNone {
				assert.Nil(
					t,
					k.Assign,
					"a structural keyword must carry no Assign: a blind copy would overwrite what the value node renders",
				)
			}

			if k.Scope == ScopeWrapper {
				assert.NotEqual(
					t,
					MergeReplace,
					k.Merge,
					"a replace-semantics keyword cannot ride the null wrapper: the split would conjoin it with the restored type value",
				)
			}

			assert.Equal(t, k.Merge == MergeNone, k.Scope == ScopeNone || !k.Asserted,
				"Merge is None exactly for the structural keywords and the ones no dispatch row asserts")

			if k.VocabRefined {
				assert.True(t, k.Asserted,
					"VocabRefined only exempts a member of a dispatch row, so it is meaningless on an unasserted row")
			}

			if k.Bound {
				assert.NotEqual(
					t,
					MergeNone,
					k.Merge,
					"a keyword in the constraint algebra composes with the type-derived value, so it cannot merge as None",
				)
			}

			if k.Name == keyword.AllOf {
				assert.Equal(
					t,
					ScopeNone,
					k.Scope,
					"allOf must stay structural: its slice carries a composite's embed branches, which a blind copy would destroy",
				)
			}
		})
	}
}

// TestKeywordMetaCoversConstants pins the bijection between the table's rows and
// internal/keyword's constants: every constant has exactly one row, and no row
// names anything else.
func TestKeywordMetaCoversConstants(t *testing.T) {
	t.Parallel()

	require.Len(t, allKeywordConstants, 55,
		"the hand-list must enumerate every internal/keyword constant")

	var names []string

	for i := range Keywords {
		names = append(names, Keywords[i].Name)
	}

	slices.Sort(names)
	require.Len(t, slices.Compact(slices.Clone(names)), len(names),
		"no keyword may be declared twice")

	want := slices.Clone(allKeywordConstants)
	slices.Sort(want)

	assert.Equal(t, want, names, "the table must declare exactly one row per keyword constant")
	assert.Len(t, ByName, len(want), "ByName must index every row")
}

// TestKeywordMetaDerivedSets pins the membership of the sets the field
// reconciliation derives from the table, by name rather than by count: a length
// assertion passes when one keyword is swapped for another, which is the mistake
// the declared columns exist to prevent. It is also the only membership check
// the bound-authorship derivation gets.
func TestKeywordMetaDerivedSets(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		keyword.Comment,
		keyword.Default,
		keyword.Deprecated,
		keyword.Description,
		keyword.Examples,
		keyword.ExclusiveMaximum,
		keyword.ExclusiveMinimum,
		keyword.MaxItems,
		keyword.MaxLength,
		keyword.MaxProperties,
		keyword.Maximum,
		keyword.MinItems,
		keyword.MinLength,
		keyword.MinProperties,
		keyword.Minimum,
		keyword.Not,
		keyword.ReadOnly,
		keyword.Title,
		keyword.UniqueItems,
		keyword.WriteOnly,
	}, Names(Movable), "Movable is the wrapper-scoped, non-replace, authorable subset")

	assert.Equal(t, []string{
		keyword.Comment,
		keyword.Const,
		keyword.ContentEncoding,
		keyword.ContentMediaType,
		keyword.ContentSchema,
		keyword.Default,
		keyword.Deprecated,
		keyword.Description,
		keyword.Enum,
		keyword.Examples,
		keyword.ExclusiveMaximum,
		keyword.ExclusiveMinimum,
		keyword.Format,
		keyword.MaxItems,
		keyword.MaxLength,
		keyword.MaxProperties,
		keyword.Maximum,
		keyword.MinItems,
		keyword.MinLength,
		keyword.MinProperties,
		keyword.Minimum,
		keyword.MultipleOf,
		keyword.Not,
		keyword.Pattern,
		keyword.ReadOnly,
		keyword.Title,
		keyword.UniqueItems,
		keyword.WriteOnly,
	}, Names(Authored), "Authored is every keyword the overlay can write from the canvas")

	assert.Equal(t, []string{
		keyword.ExclusiveMaximum,
		keyword.ExclusiveMinimum,
		keyword.MaxItems,
		keyword.MaxLength,
		keyword.MaxProperties,
		keyword.Maximum,
		keyword.MinItems,
		keyword.MinLength,
		keyword.MinProperties,
		keyword.Minimum,
		keyword.MultipleOf,
	}, Names(Bounds), "Bounds is the numeric, length, and count endpoints the algebra resolves")

	// VocabRefined is not a derived set, but its membership is pinned here for
	// the same reason: it exempts a keyword from the dispatch table's vocabulary
	// cross-check, so a row must not acquire the opt-out unnoticed.
	assert.Equal(t, []string{keyword.MaxContains, keyword.MinContains},
		Names(derive(func(k *Keyword) bool { return k.VocabRefined })),
		"only the contains counts opt out of the row vocabulary cross-check")
}
