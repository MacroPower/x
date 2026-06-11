//nolint:testpackage // white-box: tests unexported cloneSchema/isEmptySchema and the field-partition guards.
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

	// Checked-by-IsTrueSchema fields: unlike isEmptySchema, the exported
	// IsTrueSchema predicate is strict — the boolean true schema form has no
	// fields set at all, so every exported Schema field is checked and none is
	// ignored. Read directly off IsTrueSchema's body. IsFalseSchema reuses
	// IsTrueSchema for both its Not target and the sibling check, so this one
	// list protects both predicates.
	trueSchemaCheckedFields = []string{
		"ID", "Schema", "Ref", "Comment",
		"Defs", "Definitions",
		"DependencySchemas", "DependencyStrings",
		"Anchor", "DynamicAnchor", "DynamicRef", "Vocabulary",
		"Title", "Description", "Default",
		"Deprecated", "ReadOnly", "WriteOnly", "Examples",
		"Type", "Types", "Enum", "Const",
		"MultipleOf", "Minimum", "Maximum",
		"ExclusiveMinimum", "ExclusiveMaximum",
		"MinLength", "MaxLength", "Pattern",
		"PrefixItems", "Items", "ItemsArray",
		"MinItems", "MaxItems",
		"AdditionalItems", "UniqueItems", "Contains",
		"MinContains", "MaxContains", "UnevaluatedItems",
		"MinProperties", "MaxProperties",
		"Required", "DependentRequired",
		"Properties", "PatternProperties",
		"AdditionalProperties", "PropertyNames", "UnevaluatedProperties",
		"AllOf", "AnyOf", "OneOf", "Not",
		"If", "Then", "Else", "DependentSchemas",
		"ContentEncoding", "ContentMediaType", "ContentSchema",
		"Format", "Extra", "PropertyOrder",
	}
)

// TestCloneSchemaDeepIndependence verifies that cloneSchema produces an
// independent copy: mutating the clone must never reach back into the
// original. The package relies on this for remote-ref isolation: remotely
// fetched schemas are deep-copied before being registered, so Schema.Resolve's
// in-place mutations cannot corrupt the caller's schema. Because cloneSchema
// round-trips through JSON, every JSON-serializable
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

// TestIsFalseSchemaUsesEmptySchema pins isFalseSchema's definition in terms of
// isEmptySchema: a schema is the boolean-false form {"not": {}} exactly when its
// Not is non-nil and empty and the schema with Not cleared is itself empty.
// Because isFalseSchema reuses the single isEmptySchema field list, the
// isEmptySchema coverage guard already protects it; this test pins the
// observable classification so the delegation cannot silently regress.
func TestIsFalseSchemaUsesEmptySchema(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *Schema
		want   bool
	}{
		"empty not is false schema": {
			schema: &Schema{Not: &Schema{}},
			want:   true,
		},
		"nil not is not false schema": {
			schema: &Schema{},
			want:   false,
		},
		"non-empty not is not false schema": {
			schema: &Schema{Not: &Schema{Type: "string"}},
			want:   false,
		},
		"sibling keyword defeats false schema": {
			schema: &Schema{Not: &Schema{}, Type: "string"},
			want:   false,
		},
		"sibling defs defeats false schema": {
			schema: &Schema{Not: &Schema{}, Defs: map[string]*Schema{"x": {}}},
			want:   false,
		},
		"annotation siblings keep false schema": {
			schema: &Schema{Not: &Schema{}, Title: "t", Description: "d"},
			want:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			before := tc.schema.Not

			assert.Equal(t, tc.want, isFalseSchema(tc.schema))
			assert.Same(t, before, tc.schema.Not, "isFalseSchema must not mutate the schema's Not pointer")
		})
	}
}

// TestIsTrueSchemaFieldCoverage is a maintenance guard ensuring every exported
// Schema field is checked by IsTrueSchema. The strict predicate ignores
// nothing — the boolean true schema form has no fields set — so the checked
// set alone must equal the reflected field set. When upstream adds a field it
// lands outside the set and this test fails, forcing the field to be added to
// both this list and IsTrueSchema's body (where TestIsTrueSchemaRejectsEverySetField
// verifies it is actually consulted).
func TestIsTrueSchemaFieldCoverage(t *testing.T) {
	t.Parallel()

	assertFieldPartition(t, "IsTrueSchema", trueSchemaCheckedFields, nil)
}

// TestIsTrueSchemaRejectsEverySetField verifies IsTrueSchema's implementation
// (not just the classification list): for every exported Schema field, a
// schema with only that field set to a non-zero value must not be the true
// schema. Maps and slices are planted non-nil but empty, pinning the strict
// nil-versus-empty semantics — Schema{Enum: []any{}} vacuously rejects every
// instance, so present-but-empty counts as set.
func TestIsTrueSchemaRejectsEverySetField(t *testing.T) {
	t.Parallel()

	schemaType := reflect.TypeFor[Schema]()

	for i := range schemaType.NumField() {
		field := schemaType.Field(i)
		if !field.IsExported() {
			continue
		}

		t.Run(field.Name, func(t *testing.T) {
			t.Parallel()

			probe := &Schema{}
			fieldValue := reflect.ValueOf(probe).Elem().FieldByName(field.Name)

			switch field.Type.Kind() {
			case reflect.String:
				fieldValue.SetString("x")
			case reflect.Bool:
				fieldValue.SetBool(true)
			case reflect.Pointer:
				fieldValue.Set(reflect.New(field.Type.Elem()))
			case reflect.Map:
				fieldValue.Set(reflect.MakeMap(field.Type))
			case reflect.Slice:
				fieldValue.Set(reflect.MakeSlice(field.Type, 0, 0))
			default:
				t.Fatalf("Schema field %q has unhandled kind %s; extend this probe", field.Name, field.Type.Kind())
			}

			assert.False(t, IsTrueSchema(probe),
				"a schema with only %q set must not be the boolean true schema form", field.Name)
		})
	}
}

// TestIsFalseSchemaUsesTrueSchema pins the exported IsFalseSchema's definition
// in terms of IsTrueSchema: a schema is the boolean false form exactly when
// its Not is a true schema and the schema with Not cleared is itself a true
// schema. The strict predicate differs deliberately from the internal
// isFalseSchema, which tolerates annotation siblings: the exported form
// answers "does this marshal to JSON false", and a title sibling marshals to
// an object. Because IsFalseSchema reuses IsTrueSchema's single field list,
// the IsTrueSchema coverage guards protect it; this test pins the observable
// classification so the delegation cannot silently regress.
func TestIsFalseSchemaUsesTrueSchema(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *Schema
		want   bool
	}{
		"empty not is false schema": {
			schema: &Schema{Not: &Schema{}},
			want:   true,
		},
		"nil not is not false schema": {
			schema: &Schema{},
			want:   false,
		},
		"non-empty not is not false schema": {
			schema: &Schema{Not: &Schema{Type: "string"}},
			want:   false,
		},
		"sibling keyword defeats false schema": {
			schema: &Schema{Not: &Schema{}, Type: "string"},
			want:   false,
		},
		"annotation sibling defeats false schema unlike internal isFalseSchema": {
			schema: &Schema{Not: &Schema{}, Title: "t", Description: "d"},
			want:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			before := tc.schema.Not

			assert.Equal(t, tc.want, IsFalseSchema(tc.schema))
			assert.Same(t, before, tc.schema.Not, "IsFalseSchema must not mutate the schema's Not pointer")
		})
	}
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
