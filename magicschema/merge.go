package magicschema

import (
	"bytes"
	"cmp"
	"encoding/json"
	"maps"
	"math"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// mergeSchemas merges two schemas using union semantics: the result accepts
// everything either input accepts. Properties from both schemas are included
// and conflicting types are widened. Validation constraints survive the merge
// only when both sides constrain -- a side without a constraint already
// permits everything, so keeping a one-sided constraint would fail closed.
// Bounds widen toward the permissive end, enums union (a const counts as a
// single-value enum), and exact-value constraints (pattern, format,
// multipleOf, patternProperties, and other keywords with no widening rule)
// are kept only when both sides agree. A type that is null or empty on one
// side and concrete on the other widens to a [type, null] union so the null
// input still validates; that null side carries no array type evidence, so
// the typed side's items survive it (a null instance never reaches items),
// just as one-sided properties survive -- items drop only when the itemless
// side is itself array-typed and genuinely permits any element. When the two
// types are incompatible the type constraint is dropped entirely, and every
// type-specific keyword (properties, items, bounds, pattern) drops with it --
// a schema with no type but residual object or array constraints would still
// fail closed for those instances.
// Combinators and references ($ref, $dynamicRef, allOf/anyOf/oneOf, not) are
// dropped entirely, which is the most permissive behavior; the if/then/else
// conditional is kept as a unit when both sides agree exactly. Identity and
// informational keywords ($id, $comment, $anchor, $dynamicAnchor, $vocabulary)
// carry first-wins like title and description, since they annotate rather than
// constrain.
//
// The result aliases sub-structures of both inputs (one-sided properties,
// items, and kept-when-equal keywords are not cloned), so neither the
// inputs nor the merged result's shared contents may be mutated in place
// afterward; reassigning top-level fields on the result is safe.
func mergeSchemas(a, b *jsonschema.Schema) *jsonschema.Schema {
	if a == nil {
		return b
	}

	if b == nil {
		return a
	}

	// One side already widened past its type constraint because an earlier
	// merge unioned the type away -- two incompatible types, or a typeless
	// constraint schema whose constraints all dropped -- not because the value
	// was null. The result stays typeless and keeps the marker: a later fold
	// must not read the absent type as a null value and re-introduce a
	// [type, null] union that would reject the very inputs the earlier merge
	// accepted.
	if isTypelessUnion(a) || isTypelessUnion(b) {
		out := &jsonschema.Schema{}

		// Carry metadata and the value-set constraint through with the same
		// helpers the two-input incompatible path below uses, so the fold is
		// associative: which metadata and constraints survive must not depend
		// on how many inputs fold through the marked schema. A path-local copy
		// here would drop defaults, examples, x-* annotations, and enums on a
		// three-input fold while the two-input path keeps them.
		mergeMetadata(out, a, b)
		mergeValueSet(out, a, b)

		return markTypelessUnion(out)
	}

	result := &jsonschema.Schema{}

	// Merge types with widening.
	typesA, typesB := typeList(a), typeList(b)
	merged := widenTypeList(typesA, typesB)

	// An absent type on one side reads as a null or empty value, widening
	// the other side to a type-or-null union. That is right for value-derived
	// schemas, where a null records as the empty schema, but wrong for an
	// annotation-only constraint schema (pattern, enum, bounds with no type):
	// it already permits every type, so the union is typeless and injecting
	// null would claim a null is valid when neither input allowed one.
	typelessConstraint := false

	switch {
	case len(typesA) == 0 && len(typesB) > 0 && constrainsValue(a):
		merged = nil
		typelessConstraint = true

	case len(typesB) == 0 && len(typesA) > 0 && constrainsValue(b):
		merged = nil
		typelessConstraint = true

	// Two typeless sides with a constraint schema among them stay a typeless
	// union too: the constraint side already permits every type, so the
	// result must never later read as a null stand-in. Without this the
	// merge of two annotation-only schemas whose constraints all drop
	// (differing patterns, say) would return a bare empty schema that a
	// later fold widens to a [type, null] union, rejecting values the
	// constraint inputs accepted and making the fold order-dependent. Two
	// typeless sides with no constraints at all are genuine null stand-ins
	// and keep widening.
	case len(typesA) == 0 && len(typesB) == 0 && (constrainsValue(a) || constrainsValue(b)):
		merged = nil
		typelessConstraint = true
	}

	// SetSchemaType assigns the scalar Type or the Types union and dedups, so a
	// schema never carries both -- the same rule the annotators apply.
	SetSchemaType(result, merged)

	// Merge metadata and the value-set constraint (see the helpers for the
	// per-keyword rules).
	mergeMetadata(result, a, b)
	mergeValueSet(result, a, b)

	// When both sides constrain the type but the union is incompatible,
	// the type constraint is dropped entirely (fail open). Type-specific
	// validation keywords drop with it: keeping properties, items, or
	// bounds would still constrain instances of the now-unconstrained
	// union, failing closed.
	if len(merged) == 0 && len(typesA) > 0 && len(typesB) > 0 {
		return markTypelessUnion(result)
	}

	// Validation constraints: union, widen, or keep-when-equal.
	result.Pattern = keepEqual(a.Pattern, b.Pattern)
	result.Format = keepEqual(a.Format, b.Format)
	result.MultipleOf = keepEqual(a.MultipleOf, b.MultipleOf)
	result.Minimum = minPtr(a.Minimum, b.Minimum)
	result.Maximum = maxPtr(a.Maximum, b.Maximum)
	result.ExclusiveMinimum = minPtr(a.ExclusiveMinimum, b.ExclusiveMinimum)
	result.ExclusiveMaximum = maxPtr(a.ExclusiveMaximum, b.ExclusiveMaximum)
	result.MinLength = minPtr(a.MinLength, b.MinLength)
	result.MaxLength = maxPtr(a.MaxLength, b.MaxLength)
	result.MinItems = minPtr(a.MinItems, b.MinItems)
	result.MaxItems = maxPtr(a.MaxItems, b.MaxItems)
	result.MinProperties = minPtr(a.MinProperties, b.MinProperties)
	result.MaxProperties = maxPtr(a.MaxProperties, b.MaxProperties)
	result.UniqueItems = a.UniqueItems && b.UniqueItems

	// Merge object properties (union).
	if a.Properties != nil || b.Properties != nil {
		mergeProperties(result, a, b)
	}

	// Merge additionalProperties: fail-open (true wins over false).
	result.AdditionalProperties = mergeAdditionalProperties(a.AdditionalProperties, b.AdditionalProperties)

	// Merge required: intersection.
	result.Required = intersectStrings(a.Required, b.Required)

	// Merge items as a union. When both sides constrain, the element schemas
	// merge recursively. An array-typed side with no items constraint already
	// permits any element, so the union must too: grafting the other side's
	// items onto it would fail closed, and the constraint drops. The same
	// holds for a typeless constraint schema (pattern, bounds, no type): it
	// permits any array with any elements, so its instances do reach items
	// and one-sided items must drop with it. Only a side with no array
	// evidence and no constraints at all (a null or empty value whose absent
	// type widened the union to [array, null]) says nothing about elements --
	// a null instance never reaches items -- so the typed side's items
	// survive it, matching the one-sided properties an object keeps through
	// the same merge. The nil fast-path in mergeSchemas returns the non-nil
	// side, which would fail closed for two arrays, so it cannot be relied on
	// for items.
	switch {
	case a.Items != nil && b.Items != nil:
		result.Items = mergeSchemas(a.Items, b.Items)
	case a.Items != nil && !slices.Contains(typesB, typeArray) &&
		(len(typesB) > 0 || !constrainsValue(b)):
		result.Items = a.Items
	case b.Items != nil && !slices.Contains(typesA, typeArray) &&
		(len(typesA) > 0 || !constrainsValue(a)):
		result.Items = b.Items
	}

	// Keywords with no widening rule are kept only when both sides agree
	// exactly, mirroring the pattern/format/const rule: identical
	// constraints union to themselves, anything else drops (fail open).
	result.PatternProperties = keepEqual(a.PatternProperties, b.PatternProperties)
	result.PropertyNames = keepEqual(a.PropertyNames, b.PropertyNames)
	result.DependentRequired = keepEqual(a.DependentRequired, b.DependentRequired)
	result.DependentSchemas = keepEqual(a.DependentSchemas, b.DependentSchemas)
	result.DependencySchemas = keepEqual(a.DependencySchemas, b.DependencySchemas)
	result.DependencyStrings = keepEqual(a.DependencyStrings, b.DependencyStrings)
	result.UnevaluatedProperties = keepEqual(a.UnevaluatedProperties, b.UnevaluatedProperties)
	result.UnevaluatedItems = keepEqual(a.UnevaluatedItems, b.UnevaluatedItems)
	result.PrefixItems = keepEqual(a.PrefixItems, b.PrefixItems)
	result.ItemsArray = keepEqual(a.ItemsArray, b.ItemsArray)
	result.AdditionalItems = keepEqual(a.AdditionalItems, b.AdditionalItems)
	result.Contains = keepEqual(a.Contains, b.Contains)
	result.MinContains = keepEqual(a.MinContains, b.MinContains)
	result.MaxContains = keepEqual(a.MaxContains, b.MaxContains)
	result.Definitions = keepEqual(a.Definitions, b.Definitions)
	result.Defs = keepEqual(a.Defs, b.Defs)
	result.ContentEncoding = keepEqual(a.ContentEncoding, b.ContentEncoding)
	result.ContentMediaType = keepEqual(a.ContentMediaType, b.ContentMediaType)
	result.ContentSchema = keepEqual(a.ContentSchema, b.ContentSchema)

	// The if/then/else conditional only has meaning as a unit, so it is
	// kept only when the whole trio agrees.
	if reflect.DeepEqual(a.If, b.If) &&
		reflect.DeepEqual(a.Then, b.Then) &&
		reflect.DeepEqual(a.Else, b.Else) {
		result.If, result.Then, result.Else = a.If, a.Then, a.Else
	}

	// The typeless-constraint union above drops the type because the
	// annotation-only side already permits every type, not because a value
	// was null. When every constraint also drops (one-sided pattern, bounds,
	// or enum leave nothing behind), the result is indistinguishable from a
	// null-value schema, so it carries the typeless-union marker to keep a
	// later fold from re-reading the absent type as a null and emitting a
	// [type, null] union that rejects values this merge accepts. A result
	// that still constrains needs no marker: the constrainsValue check above
	// keeps the next fold typeless.
	if typelessConstraint && !constrainsValue(result) {
		return markTypelessUnion(result)
	}

	return result
}

// mergeMetadata carries the informational keywords of a and b onto out with
// union semantics: prefer a, fall back to b. Both merge paths -- the main
// path and the typeless-union fast path -- share it, so the metadata a marked
// schema carries does not depend on how many inputs fold through it.
func mergeMetadata(out, a, b *jsonschema.Schema) {
	out.Title = cmp.Or(a.Title, b.Title)
	out.Description = cmp.Or(a.Description, b.Description)

	// Identity and informational keywords annotate rather than constrain, so
	// they carry first-wins like title and description. The annotator-merge
	// path (mergeSchemaFields) already keeps them; a later union merge must not
	// silently drop what survived single-input generation. References ($ref,
	// $dynamicRef) stay dropped (see the mergeSchemas doc comment).
	out.Schema = cmp.Or(a.Schema, b.Schema)
	out.ID = cmp.Or(a.ID, b.ID)
	out.Comment = cmp.Or(a.Comment, b.Comment)
	out.Anchor = cmp.Or(a.Anchor, b.Anchor)
	out.DynamicAnchor = cmp.Or(a.DynamicAnchor, b.DynamicAnchor)

	// Same first-non-nil-wins policy as the cmp.Or fields above, spelled out
	// because Vocabulary (map), Default (json.RawMessage), and Examples ([]any)
	// are not comparable and cmp.Or rejects them.
	if a.Vocabulary != nil {
		out.Vocabulary = a.Vocabulary
	} else {
		out.Vocabulary = b.Vocabulary
	}

	if a.Default != nil {
		out.Default = a.Default
	} else {
		out.Default = b.Default
	}

	if a.Examples != nil {
		out.Examples = a.Examples
	} else {
		out.Examples = b.Examples
	}

	// Deprecated is informational and sticky; readOnly/writeOnly restrict
	// usage, so they hold only when both sides agree.
	out.Deprecated = a.Deprecated || b.Deprecated
	out.ReadOnly = a.ReadOnly && b.ReadOnly
	out.WriteOnly = a.WriteOnly && b.WriteOnly

	// Merge x-* custom annotations per key, a wins.
	out.Extra = mergeExtra(a.Extra, b.Extra)
}

// mergeValueSet carries the unioned value-set constraint of a and b onto out.
// Enum and const both constrain value sets (const is a single-value enum), so
// they union with each other, independent of type. Equal consts stay const;
// any other both-sides combination unions to enum. An unconstrained side
// widens the union to accept everything (fail open), since unionEnums yields
// nil whenever either side is unconstrained.
func mergeValueSet(out, a, b *jsonschema.Schema) {
	if a.Const != nil && b.Const != nil && jsonValueEqual(*a.Const, *b.Const) {
		out.Const = a.Const
	} else {
		out.Enum = unionEnums(enumValues(a), enumValues(b))
	}
}

// jsonValueEqual reports whether two decoded values are equal by their
// canonical JSON encoding. Numerically equal values may arrive as different
// Go types -- goccy parses an integer literal to uint64 while a float literal
// or a [ToSubSchema] JSON round trip yields float64 -- and [reflect.DeepEqual]
// treats those as distinct even though they marshal to identical JSON bytes,
// which would emit duplicate enum members the spec wants unique. Values that
// cannot be marshaled fall back to [reflect.DeepEqual].
func jsonValueEqual(a, b any) bool {
	ab, aErr := json.Marshal(a)
	bb, bErr := json.Marshal(b)

	if aErr != nil || bErr != nil {
		return reflect.DeepEqual(a, b)
	}

	return bytes.Equal(ab, bb)
}

// typelessUnionKey marks a schema whose type was dropped because the union
// accepts every type -- two incompatible types unioned, or a typeless
// annotation-only constraint schema met a typed one and its constraints all
// dropped -- as opposed to an absent type that stands in for a null value.
// The merge reads an absent type on one side as a null and widens the other
// side to a [type, null] union; without this marker a three-input fold such
// as string + integer + string would read the string+integer result
// (typeless) as a null and re-emit [string, null], rejecting the integer
// input and falsely admitting null. The marker rides along through the fold
// and stripTypelessUnion removes it before output, so it never reaches the
// generated schema.
const typelessUnionKey = "__magicschema_typeless_union__"

// markTypelessUnion records the typeless-union marker on s and returns it.
func markTypelessUnion(s *jsonschema.Schema) *jsonschema.Schema {
	if s.Extra == nil {
		s.Extra = make(map[string]any, 1)
	}

	s.Extra[typelessUnionKey] = true

	return s
}

// isTypelessUnion reports whether s carries the typeless-union marker.
func isTypelessUnion(s *jsonschema.Schema) bool {
	if s == nil || s.Extra == nil {
		return false
	}

	_, ok := s.Extra[typelessUnionKey]

	return ok
}

// stripTypelessUnion removes the internal typeless-union marker from the whole
// schema tree before output. A marked schema carries no nested sub-schemas in
// type-specific positions (items, properties, and the like) -- only the
// informational keywords mergeMetadata carries (title, description, identity
// keywords, default, examples, usage flags, x-* annotations) and at most an
// Enum or Const value set -- so reaching every schema position via
// forEachSubSchema is enough to clear the marker; none of the carried fields
// hold a sub-schema, so they need no descent.
//
// The walk crosses sub-schemas the merge only aliases from annotator-owned
// prototypes, which copySchema promises never to write into (and which a
// concurrently reused annotator may share across Generate calls), so a schema
// without the marker must not be touched at all: even a no-op delete on its
// Extra map is a map write and a data race. Only merge-allocated schemas ever
// carry the marker, and those are safe to mutate.
func stripTypelessUnion(s *jsonschema.Schema) {
	if s == nil {
		return
	}

	if isTypelessUnion(s) {
		delete(s.Extra, typelessUnionKey)

		if len(s.Extra) == 0 {
			s.Extra = nil
		}
	}

	forEachSubSchema(s, stripTypelessUnion)
}

// keepEqual returns the shared value when both sides agree exactly, or the
// zero value otherwise. A constraint present on only one side, or differing
// between sides, drops from the union (fail open).
func keepEqual[T any](a, b T) T {
	if reflect.DeepEqual(a, b) {
		return a
	}

	var zero T

	return zero
}

// enumValues returns a schema's value-set constraint: the enum, or the
// const as a single-value enum.
func enumValues(s *jsonschema.Schema) []any {
	if s.Enum != nil {
		return s.Enum
	}

	if s.Const != nil {
		return []any{*s.Const}
	}

	return nil
}

// unionEnums merges enum constraints. The value set is kept only when both
// sides constrain: an unconstrained side already allows everything, so the
// union has no enum at all.
//
// When the two enums differ, the merged set is sorted by each value's JSON
// encoding so that merging equivalent inputs in a different order yields a
// byte-identical enum array, mirroring the sort intersectStrings applies to
// the required array; an enum is an unordered set in JSON Schema, so the
// canonical order changes nothing semantically. When the two enums are
// identical -- the common case of one annotation reused across the dynamic
// keys of a merged mapping -- the author's order is preserved as-is, since
// there is no merge-order ambiguity to resolve. Values JSON cannot encode keep
// their existing relative order rather than failing the merge (fail open).
func unionEnums(a, b []any) []any {
	if a == nil || b == nil {
		return nil
	}

	if slices.EqualFunc(a, b, jsonValueEqual) {
		return slices.Clone(a)
	}

	out := slices.Clone(a)

	for _, v := range b {
		if !slices.ContainsFunc(out, func(x any) bool { return jsonValueEqual(x, v) }) {
			out = append(out, v)
		}
	}

	slices.SortStableFunc(out, func(x, y any) int {
		xb, xErr := json.Marshal(x)
		yb, yErr := json.Marshal(y)

		if xErr != nil || yErr != nil {
			return 0
		}

		return bytes.Compare(xb, yb)
	})

	return out
}

// valueSetFitsType reports whether every value in the set is valid for the
// schema's type constraint. A schema with no type accepts any value set, and a
// value whose decoded Go type maps to no JSON type is treated as a fit (fail
// open). It lets a merge skip grafting a lower-priority const or enum onto a
// higher-priority incompatible type, which would reject every value.
func valueSetFitsType(values []any, s *jsonschema.Schema) bool {
	types := typeList(s)
	if len(types) == 0 {
		return true
	}

	for _, v := range values {
		jts := jsonTypesOf(v)
		if len(jts) == 0 {
			continue
		}

		if !slices.ContainsFunc(jts, func(jt string) bool { return slices.Contains(types, jt) }) {
			return false
		}
	}

	return true
}

// jsonTypesOf returns the JSON Schema type tokens a decoded YAML/JSON value may
// satisfy. A whole-number float counts as both number and integer; an empty
// result means the Go type is unknown and the caller should treat it as a fit.
func jsonTypesOf(v any) []string {
	switch n := v.(type) {
	case nil:
		return []string{typeNull}
	case bool:
		return []string{typeBoolean}
	case string:
		return []string{typeString}
	case float32:
		return floatTypes(float64(n))
	case float64:
		return floatTypes(n)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return []string{typeNumber, typeInteger}
	case []any:
		return []string{typeArray}
	case map[string]any:
		return []string{typeObject}
	default:
		return nil
	}
}

// floatTypes classifies a float: a finite whole number satisfies both number
// and integer, anything else only number.
func floatTypes(f float64) []string {
	if !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f) {
		return []string{typeNumber, typeInteger}
	}

	return []string{typeNumber}
}

// mergeExtra merges two x-* annotation maps per key, with a winning conflicts.
// Cloning a first keeps the inputs untouched; mergeExtraInto then fills in b's
// keys that a lacks, which is the same first-wins union.
func mergeExtra(a, b map[string]any) map[string]any {
	return mergeExtraInto(maps.Clone(a), b)
}

// mergeExtraInto folds src into dst in place, keeping dst's value on a key
// conflict (first-wins), and allocating dst when it is nil and src is not. It
// returns the resulting map. Unlike mergeExtra it mutates dst rather than
// allocating a fresh map, for callers filling a higher-priority schema's x-*
// annotations from a lower-priority one.
func mergeExtraInto(dst, src map[string]any) map[string]any {
	if src == nil {
		return dst
	}

	if dst == nil {
		dst = make(map[string]any, len(src))
	}

	for k, v := range src {
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}

	return dst
}

// minPtr returns the smaller of two bounds, or nil if either side is
// unconstrained.
func minPtr[T cmp.Ordered](a, b *T) *T {
	if a == nil || b == nil {
		return nil
	}

	if *b < *a {
		return b
	}

	return a
}

// maxPtr returns the larger of two bounds, or nil if either side is
// unconstrained.
func maxPtr[T cmp.Ordered](a, b *T) *T {
	if a == nil || b == nil {
		return nil
	}

	if *b > *a {
		return b
	}

	return a
}

// mergeAdditionalProperties merges two additionalProperties values.
// Uses fail-open semantics: if either side allows additional properties,
// the result allows them. In JSON Schema, nil (unset) means no constraint,
// which is equivalent to allowing everything. A false schema yields to the
// other side (the union of "nothing extra" and a constraint is the
// constraint), and two constrained schemas merge recursively.
func mergeAdditionalProperties(a, b *jsonschema.Schema) *jsonschema.Schema {
	if a == nil && b == nil {
		return nil
	}

	// Nil means unset, which defaults to allowing everything in JSON Schema.
	// Per fail-open semantics: nil or true schema on either side means the
	// result allows additional properties.
	if a == nil || b == nil || isTrueSchema(a) || isTrueSchema(b) {
		return TrueSchema()
	}

	if isFalseSchema(a) {
		return b
	}

	if isFalseSchema(b) {
		return a
	}

	return mergeSchemas(a, b)
}

// isTrueSchema checks if a schema is the "true" schema (validates
// everything): the zero schema with no constraints, metadata, or extensions
// at all. A schema carrying only value constraints (pattern, enum, const,
// bounds) or x-* annotations is not "true" -- collapsing it would silently
// drop the constraint.
func isTrueSchema(s *jsonschema.Schema) bool {
	if s == nil {
		return false
	}

	return reflect.DeepEqual(*s, jsonschema.Schema{})
}

// isFalseSchema checks if a schema is the "false" schema (validates nothing),
// i.e. exactly {"not": {}} as produced by [FalseSchema], with no other
// constraints, metadata, or extensions.
func isFalseSchema(s *jsonschema.Schema) bool {
	if s == nil || !isTrueSchema(s.Not) {
		return false
	}

	c := *s
	c.Not = nil

	return reflect.DeepEqual(c, jsonschema.Schema{})
}

// constrainsValue reports whether a typeless schema carries a value
// constraint -- pattern, enum, bounds, sub-schemas, and the like -- rather
// than being an empty or metadata-only placeholder for a null or absent
// value. The merge uses it to tell an absent type that means the value was
// null (widen to a type-or-null union) from an absent type on an
// annotation-only constraint schema that already permits every type (so the
// union is typeless). Metadata such as title, description, default, and
// examples does not constrain the value set and is ignored.
//
// Every value-constraining keyword counts, including the object- and
// array-shaped ones (additionalProperties, contains, propertyNames,
// if/then/else, ...): a typeless schema constrained only by one of them still
// permits every other type, so widening it to a [type, null] union would
// reject values it currently accepts (fail closed).
func constrainsValue(s *jsonschema.Schema) bool {
	return s.Pattern != "" || s.Format != "" ||
		s.Enum != nil || s.Const != nil ||
		s.Minimum != nil || s.Maximum != nil ||
		s.ExclusiveMinimum != nil || s.ExclusiveMaximum != nil ||
		s.MultipleOf != nil ||
		s.MinLength != nil || s.MaxLength != nil ||
		s.MinItems != nil || s.MaxItems != nil || s.UniqueItems ||
		s.Items != nil || s.PrefixItems != nil || s.ItemsArray != nil ||
		s.AdditionalItems != nil || s.Contains != nil ||
		s.MinContains != nil || s.MaxContains != nil || s.UnevaluatedItems != nil ||
		s.MinProperties != nil || s.MaxProperties != nil ||
		s.Properties != nil || s.PatternProperties != nil ||
		s.AdditionalProperties != nil || s.PropertyNames != nil ||
		s.UnevaluatedProperties != nil ||
		len(s.Required) > 0 ||
		s.DependentRequired != nil || s.DependentSchemas != nil ||
		s.DependencySchemas != nil || s.DependencyStrings != nil ||
		s.ContentEncoding != "" || s.ContentMediaType != "" || s.ContentSchema != nil ||
		s.AllOf != nil || s.AnyOf != nil || s.OneOf != nil || s.Not != nil ||
		s.If != nil || s.Then != nil || s.Else != nil
}

// intersectStrings returns the intersection of two string slices.
func intersectStrings(a, b []string) []string {
	if a == nil || b == nil {
		return nil
	}

	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}

	var result []string

	for _, s := range b {
		if set[s] {
			result = append(result, s)
		}
	}

	if len(result) == 0 {
		return nil
	}

	// Sort so the merged required array does not depend on input order: the
	// intersection is otherwise built in b's iteration order, so swapping two
	// equivalent input files would emit a byte-different required array,
	// breaking the deterministic-output guarantee. The required array is an
	// unordered set in JSON Schema, so sorting changes nothing semantically.
	slices.Sort(result)

	return result
}

// propertyKeys returns property keys in PropertyOrder, then any remaining
// keys sorted lexically so the result is deterministic.
func propertyKeys(s *jsonschema.Schema) []string {
	if s.Properties == nil {
		return nil
	}

	if len(s.PropertyOrder) > 0 {
		seen := make(map[string]bool, len(s.PropertyOrder))

		var keys []string

		for _, k := range s.PropertyOrder {
			if _, ok := s.Properties[k]; ok {
				keys = append(keys, k)
				seen[k] = true
			}
		}

		var rest []string

		for k := range s.Properties {
			if !seen[k] {
				rest = append(rest, k)
			}
		}

		slices.Sort(rest)

		return append(keys, rest...)
	}

	return slices.Sorted(maps.Keys(s.Properties))
}

// mergeProperties merges properties from a and b into result using union semantics.
func mergeProperties(result, a, b *jsonschema.Schema) {
	result.Properties = make(map[string]*jsonschema.Schema)

	var order []string

	// Add all from a first.
	if a.Properties != nil {
		for _, k := range propertyKeys(a) {
			result.Properties[k] = a.Properties[k]
			order = append(order, k)
		}
	}

	// Merge from b.
	if b.Properties != nil {
		for _, k := range propertyKeys(b) {
			if existing, ok := result.Properties[k]; ok {
				result.Properties[k] = mergeSchemas(existing, b.Properties[k])
			} else {
				result.Properties[k] = b.Properties[k]
				order = append(order, k)
			}
		}
	}

	result.PropertyOrder = order
}
