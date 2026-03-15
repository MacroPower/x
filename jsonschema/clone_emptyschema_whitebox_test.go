//nolint:testpackage // white-box: tests unexported cloneSchema/isEmptySchema/isSchemaTrivial.
package jsonschema

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Field classifications used by the maintenance guards below. Grouped into a
// single var block so adding a set is a deliberate edit and golangci's grouper
// stays satisfied.
var (
	// JSON-untagged exported Schema fields (struct tag `json:"-"`) are excluded
	// from the json-tag coverage guard because the upstream *Schema implements a
	// custom MarshalJSON/UnmarshalJSON that renders them under their real keywords
	// ("type", "items", "dependencies", arbitrary Extra keys), so cloneSchema's
	// JSON round-trip still carries them. PropertyOrder is the lone exception: a
	// render-only ordering hint the custom marshaler drops, whose loss is
	// documented on cloneSchema and is acceptable because it carries no validation
	// semantics. Each entry's value documents the reason.
	jsonUntaggedFields = map[string]string{
		"Type":              "custom MarshalJSON renders as \"type\"",
		"Types":             "custom MarshalJSON renders as \"type\" (array form)",
		"Items":             "custom MarshalJSON renders as \"items\"",
		"ItemsArray":        "custom MarshalJSON renders as \"items\" (array form)",
		"Extra":             "custom MarshalJSON inlines each key as a top-level keyword",
		"DependencySchemas": "custom MarshalJSON renders under \"dependencies\"",
		"DependencyStrings": "custom MarshalJSON renders under \"dependencies\"",
		"PropertyOrder":     "render-only ordering hint, intentionally dropped by cloneSchema (no validation semantics)",
	}

	// Checked-by-isEmptySchema fields: the Schema fields isEmptySchema inspects to
	// decide a schema sets no validation keywords (accept-all). Read directly off
	// isEmptySchema's body; a schema is empty only if all of these are zero.
	emptySchemaCheckedFields = []string{
		"Type", "Types", "Ref", "DynamicRef",
		"Properties", "Required", "Items", "PrefixItems",
		"AllOf", "AnyOf", "OneOf", "Not",
		"If", "Then", "Else",
		"Enum", "Const",
		"Minimum", "Maximum", "ExclusiveMinimum", "ExclusiveMaximum",
		"MinLength", "MaxLength", "Pattern", "Format",
		"MinItems", "MaxItems", "UniqueItems",
		"MinProperties", "MaxProperties",
		"AdditionalProperties", "AdditionalItems",
		"PatternProperties", "PropertyNames", "Contains",
		"MultipleOf",
		"UnevaluatedProperties", "UnevaluatedItems",
		"DependentRequired", "DependentSchemas",
		"DependencySchemas", "DependencyStrings",
		"MinContains", "MaxContains",
		"Defs", "Definitions",
		"ContentEncoding", "ContentMediaType",
	}

	// Ignored-by-isEmptySchema fields: annotation/metadata keywords (Title,
	// Description) that don't constrain instances, plus a few applicator-shaped
	// fields the detector does not consult: it checks Items but neither
	// ItemsArray nor ContentSchema, and that omission is consistent with how those
	// fields are used: ItemsArray (draft-07 tuple items) is only consulted by the
	// walk under Draft7, while isEmptySchema's callers (unevaluatedProperties /
	// unevaluatedItems) are 2020-12-only keywords; ContentSchema backs the
	// annotation-only contentSchema keyword, which never asserts. A schema whose
	// only keyword is one of these is therefore treated as accept-all, matching the
	// walk's behavior. The guard pins this; revisit the classification if the walk
	// ever consults these fields where isEmptySchema runs.
	emptySchemaIgnoredFields = []string{
		"Schema", "ID", "Anchor", "DynamicAnchor", "Comment",
		"Title", "Description", "Default", "Examples",
		"ReadOnly", "WriteOnly", "Deprecated", "Vocabulary",
		"Extra", "PropertyOrder",
		"ItemsArray", "ContentSchema",
	}

	// Checked-by-isSchemaTrivial fields: the fields isSchemaTrivial inspects. It
	// mirrors isEmptySchema but allows Not to be set (a "false" schema is
	// {not: {}}), so Not moves to the ignored set; it also stops short of the
	// content keywords and $defs/definitions that isEmptySchema checks.
	trivialSchemaCheckedFields = []string{
		"Type", "Types", "Ref", "DynamicRef",
		"Properties", "Required", "Items", "PrefixItems",
		"AllOf", "AnyOf", "OneOf",
		// Not is intentionally absent: trivial schemas allow Not to be set.
		"If", "Then", "Else",
		"Enum", "Const",
		"Minimum", "Maximum", "ExclusiveMinimum", "ExclusiveMaximum",
		"MinLength", "MaxLength", "Pattern", "Format",
		"MinItems", "MaxItems", "UniqueItems",
		"MinProperties", "MaxProperties",
		"AdditionalProperties", "AdditionalItems",
		"PatternProperties", "PropertyNames", "Contains",
		"MultipleOf",
		"UnevaluatedProperties", "UnevaluatedItems",
		"DependentRequired", "DependentSchemas",
		"DependencySchemas", "DependencyStrings",
		"MinContains", "MaxContains",
	}

	// Ignored-by-isSchemaTrivial fields: the same annotation/metadata set as
	// isEmptySchema, plus Not (allowed for false schemas) and the content/$defs
	// keywords isSchemaTrivial does not inspect.
	trivialSchemaIgnoredFields = []string{
		"Schema", "ID", "Anchor", "DynamicAnchor", "Comment",
		"Title", "Description", "Default", "Examples",
		"ReadOnly", "WriteOnly", "Deprecated", "Vocabulary",
		"Extra", "PropertyOrder",
		"ItemsArray", "ContentSchema",
		"Not", // Not is allowed — that is what makes a schema "false".
		"Defs", "Definitions", "ContentEncoding", "ContentMediaType",
	}
)

// TestCloneSchemaDeepIndependence verifies that cloneSchema produces an
// independent copy: mutating the clone must never reach back into the
// original. The package relies on this for remote-ref isolation (PRD Thread
// Safety: "remotely-fetched schemas are deep-copied before being registered"),
// where Schema.Resolve's in-place mutations must not corrupt the caller's
// schema. Because cloneSchema round-trips through JSON, every JSON-serializable
// field — including maps, slices, and pointers shared shallowly by upstream's
// CloneSchemas — comes back as a fresh value. These cases verify that
// independence directly rather than documenting upstream's shallow CloneSchemas
// as a known gap.
func TestCloneSchemaDeepIndependence(t *testing.T) {
	t.Parallel()

	// Build a schema whose Const points at a fresh value, so each case owns its
	// pointer and mutating the clone's *Const can't alias another case's value.
	constSchema := func() *Schema {
		var v any = "a"

		return &Schema{Const: &v}
	}

	tests := map[string]struct {
		schema *Schema
		mutate func(clone *Schema)
		check  func(t *testing.T, original *Schema)
	}{
		"extra nested map": {
			schema: &Schema{Extra: map[string]any{"x-custom": map[string]any{"nested": "value"}}},
			mutate: func(clone *Schema) {
				if nested, ok := clone.Extra["x-custom"].(map[string]any); ok {
					nested["nested"] = "modified"
				}
			},
			check: func(t *testing.T, original *Schema) {
				t.Helper()

				nested, ok := original.Extra["x-custom"].(map[string]any)
				require.True(t, ok, "x-custom should round-trip as a map[string]any")
				assert.Equal(t, "value", nested["nested"], "nested map inside Extra must be independent of the clone")
			},
		},
		"extra top-level value": {
			schema: &Schema{Extra: map[string]any{"x-custom": "value"}},
			mutate: func(clone *Schema) { clone.Extra["x-custom"] = "modified" },
			check: func(t *testing.T, original *Schema) {
				t.Helper()

				assert.Equal(t, "value", original.Extra["x-custom"],
					"Extra map must not share backing storage with the clone")
			},
		},
		"enum slice element": {
			schema: &Schema{Enum: []any{"a", "b"}},
			mutate: func(clone *Schema) { clone.Enum[0] = "modified" },
			check: func(t *testing.T, original *Schema) {
				t.Helper()

				assert.Equal(t, "a", original.Enum[0], "Enum slice must not share backing storage with the clone")
			},
		},
		"examples slice element": {
			schema: &Schema{Examples: []any{"a", "b"}},
			mutate: func(clone *Schema) { clone.Examples[0] = "modified" },
			check: func(t *testing.T, original *Schema) {
				t.Helper()

				assert.Equal(t, "a", original.Examples[0],
					"Examples slice must not share backing storage with the clone")
			},
		},
		"const pointer": {
			schema: constSchema(),
			mutate: func(clone *Schema) { *clone.Const = "modified" },
			check: func(t *testing.T, original *Schema) {
				t.Helper()

				require.NotNil(t, original.Const)
				assert.Equal(t, "a", *original.Const, "Const pointer must address a distinct value from the clone")
			},
		},
		"default raw message byte": {
			schema: &Schema{Default: json.RawMessage(`"a"`)},
			mutate: func(clone *Schema) { clone.Default[1] = 'X' },
			check: func(t *testing.T, original *Schema) {
				t.Helper()

				assert.Equal(t, `"a"`, string(original.Default),
					"Default bytes must not share backing storage with the clone")
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			clone, err := cloneSchema(tc.schema)
			require.NoError(t, err)
			require.NotSame(t, tc.schema, clone, "clone must be a distinct *Schema")

			tc.mutate(clone)
			tc.check(t, tc.schema)
		})
	}
}

// TestCloneSchemaSerializableFieldCoverage is a maintenance guard over the JSON
// round-trip in cloneSchema. Every exported Schema field must either carry a
// non-empty `json` struct tag (so a plain Marshal/Unmarshal can't silently drop
// it) or be explicitly allowlisted in jsonUntaggedFields with a reason. When
// upstream adds a new exported field with `json:"-"` and no custom-marshaler
// coverage, this test fails and forces a maintainer to decide whether cloneSchema
// still copies it.
func TestCloneSchemaSerializableFieldCoverage(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[Schema]()

	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		tag, ok := field.Tag.Lookup("json")
		if !ok || tag == "" || tag == "-" {
			reason, allowlisted := jsonUntaggedFields[field.Name]
			require.True(t, allowlisted,
				"exported Schema field %q has no usable json tag (%q) and is not allowlisted; "+
					"classify it: either it round-trips via the custom marshaler or cloneSchema drops it",
				field.Name, tag)
			assert.NotEmpty(t, reason,
				"allowlist entry for %q must document why the missing json tag is acceptable", field.Name)

			continue
		}

		// A tagged field carries at least a key (e.g. "minimum,omitempty"); the
		// leading segment before any comma must be non-empty.
		name, _, _ := strings.Cut(tag, ",")
		assert.NotEmpty(t, name, "exported Schema field %q has a json tag with an empty name: %q", field.Name, tag)
	}
}

// TestIsEmptySchemaFieldCoverage is a maintenance guard ensuring every exported
// Schema field is classified as either checked by isEmptySchema or deliberately
// ignored. The union of the two hardcoded sets must equal the reflected field
// set, and the sets must be disjoint. When upstream adds a new field it lands in
// neither set and this test fails, forcing a maintainer to decide whether the new
// keyword affects emptiness. This supersedes the empty marker
// TestIsEmptySchemaMaintenanceFragility.
func TestIsEmptySchemaFieldCoverage(t *testing.T) {
	t.Parallel()

	assertFieldPartition(t, "isEmptySchema", emptySchemaCheckedFields, emptySchemaIgnoredFields)
}

// TestIsSchemaTrivialFieldCoverage is the maintenance guard for isSchemaTrivial,
// the predicate behind isFalseSchema. Like the isEmptySchema guard, it asserts
// the checked and ignored sets partition the full reflected field set so a new
// upstream field forces explicit classification. Note Not lives in the ignored
// set here because a trivial (false) schema is exactly {not: {}}.
func TestIsSchemaTrivialFieldCoverage(t *testing.T) {
	t.Parallel()

	assertFieldPartition(t, "isSchemaTrivial", trivialSchemaCheckedFields, trivialSchemaIgnoredFields)
}

// assertFieldPartition asserts that checked and ignored together cover every
// exported Schema field exactly once. The label argument names the predicate
// under guard so failures read clearly.
func assertFieldPartition(t *testing.T, label string, checked, ignored []string) {
	t.Helper()

	reflected := exportedSchemaFieldNames()

	union := map[string]int{}
	for _, name := range checked {
		union[name]++
	}

	for _, name := range ignored {
		union[name]++
	}

	// Disjoint: no field appears in both sets.
	var overlap []string
	for name, count := range union {
		if count > 1 {
			overlap = append(overlap, name)
		}
	}

	sort.Strings(overlap)
	assert.Empty(t, overlap, "%s: fields classified as both checked and ignored: %v", label, overlap)

	// Coverage: the union of the two sets equals the reflected field set.
	classified := make(map[string]bool, len(union))
	for name := range union {
		classified[name] = true
	}

	var missing []string
	for _, name := range reflected {
		if !classified[name] {
			missing = append(missing, name)
		}
	}

	sort.Strings(missing)
	require.Empty(t, missing,
		"%s: exported Schema field(s) %v are classified as neither checked nor ignored; "+
			"a new upstream keyword must be added to one of the two sets after deciding whether it affects the predicate",
		label, missing)

	// Stale: every classified name still exists on the struct.
	reflectedSet := make(map[string]bool, len(reflected))
	for _, name := range reflected {
		reflectedSet[name] = true
	}

	var stale []string
	for name := range union {
		if !reflectedSet[name] {
			stale = append(stale, name)
		}
	}

	sort.Strings(stale)
	assert.Empty(t, stale, "%s: classified field(s) %v no longer exist on Schema", label, stale)
}

// exportedSchemaFieldNames returns the names of all exported Schema fields.
func exportedSchemaFieldNames() []string {
	typ := reflect.TypeFor[Schema]()

	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.IsExported() {
			names = append(names, field.Name)
		}
	}

	return names
}
