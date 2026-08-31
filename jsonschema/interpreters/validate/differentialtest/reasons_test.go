package differentialtest_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The reason constants name every case this rig does not compare, so widening
// the record is a diff a reviewer can read rather than a doc comment they can
// miss. Each states a divergence the interpreter takes by design, a shape
// go-playground itself cannot serve as a reference for, or a region where the
// agreement property is weaker than the biconditional.
const (
	// Go-playground applies required to a bare slice or map as a nil check, so
	// an empty but non-nil collection passes, while the interpreter floors the
	// size and rejects that collection. A byte slice diverges the same way with
	// minLength standing in for minItems, since go-playground reads the slice it
	// is while the schema measures the base64 string it becomes. The exclusion
	// covers that empty collection and nothing else. The nil side of the same
	// rule agrees, because the size floor rejects the empty instance a nil
	// collection marshals as under encoding/json/v2, and
	// FuzzValidatorRequiredNullableShapes compares it.
	reasonRequiredCollectionEmptyFloor = "a non-nil empty collection satisfies go-playground's required and trips the schema's size floor"
	// An omitempty field whose encoded value is empty is absent from the
	// marshaled object even when its Go value is set: encoding/json/v2 drops a
	// non-nil pointer to an empty string, which go-playground's required
	// accepts as present. The required entry then rejects the object for the
	// missing key, making the schema stricter than go-playground where the
	// weakened property demands it be more permissive, so the draw skips the
	// pairing.
	reasonRequiredOmitemptyDropped = "required beside omitempty rejects an object encoding/json/v2 dropped a set field from"
	// Go-playground dereferences a pointer and rejects a nil collection behind
	// it, but encoding/json/v2 marshals that nil as its empty instance,
	// byte-identical to the allocated empty go-playground accepts, so no
	// schema can state the distinction and the target skips the value.
	reasonRequiredPointerNilCollection = "a nil collection behind a non-nil pointer marshals identically to the allocated empty go-playground accepts"
	// Go-playground's oneof formats the field as text and handles only the
	// string and integer kinds, panicking with "Bad field type" on anything
	// else, so it cannot be the reference for oneof on a bool or a float.
	reasonOneOfKindPanic = "go-playground panics on oneof against any kind but a string or an integer"
	// Go-playground's unique on a map means its values are distinct, which has
	// no object-side counterpart to uniqueItems, so the interpreter emits
	// nothing and the two cannot agree on a duplicate-valued map.
	reasonUniqueMapNoOp = "unique on a map is a documented no-op with no object-side keyword"
	// A numeric bound has no faithful mapping onto the quoted instance a
	// json:",string" field emits, so the interpreter rejects it at generation
	// rather than emitting an inert numeric keyword on a string schema. The
	// draw spells it and the rig checks the rejection instead of a verdict.
	reasonCoercedNumericBounds = "a numeric bound on a coerced field is rejected at generation, not modeled"
	// Required on a bool pins the value to true, which contradicts a const of
	// false, so the interpreter reports the pair as a conflict at generation.
	// The draw places one rule per field, which is what keeps the pair
	// unreachable.
	reasonRequiredEqFalseConflict = "required with eq=false on a bool is a generation-time conflict"
	// Go-playground matches a format with its own regexes while the schema
	// delegates to internal/format, so the two disagree on RFC edge cases by
	// construction. That surface has its own corpora in internal/format.
	reasonFormatDelegated = "format checking is go-playground's regexes against internal/format, a separate surface"
	// The pattern tags map to a fixed regex the schema then evaluates under Go
	// RE2, which is not the engine go-playground matches with.
	reasonPatternDialect = "a pattern tag is evaluated by two different regex engines"
	// The content tags are runtime checks over the raw bytes on one side and
	// keywords describing an encoded string on the other, and on a raw JSON
	// field the interpreter documents both as no-ops.
	reasonContentUnmodeled = "the content tags describe an encoded string, not the value go-playground checks"
	// The constraints inside a keys...endkeys block apply to map keys, which
	// the interpreter does not model at all.
	reasonKeysBlockUnmodeled = "map-key constraints inside keys...endkeys are not modeled"
	// A cross-field or conditional validator reads a sibling field, which no
	// single property schema can express.
	reasonCrossFieldUnmodeled = "a cross-field or conditional validator reads a sibling the schema cannot see"
	// Within one comma group the pipe separates OR alternatives, of which the
	// interpreter reads only the first.
	reasonOrOperatorUnmodeled = "the | OR operator is not modeled; only the first alternative is read"
	// An omitempty or omitzero field at its zero is absent from the marshaled
	// object, where the schema imposes nothing unless the field is required,
	// while go-playground still validates the Go zero value.
	reasonOmitemptyDropsField = "an absent field leaves the schema nothing to assert, so it can only be more permissive"
	// A JSON Schema keyword is type-conditional. The minLength keyword says
	// nothing about a null instance, so every value rule passes on one. Go-playground has no
	// such rule and rejects a nil pointer under any constraint. The interpreter
	// takes the schema side deliberately, landing a value constraint on the
	// value branch of the null encoding so a permitted null stays valid.
	reasonNullableValueRule = "a value rule passes on a null instance in JSON Schema and fails on a nil pointer in go-playground"
)

// rigExclusion is one case the rig does not compare. Where it names a rule the
// entry is checkable against the draw pools, which is what keeps the record
// from drifting away from what the draw actually does; where it does not, the
// entry is the written record of a weakening the harness applies.
type rigExclusion struct {
	what   string
	reason string
	rule   string
	// The kinds field narrows a rule exclusion to particular field types. An empty
	// list with a rule set means the draw never spells that rule for any type.
	kinds []reflect.Type
	// The covered field applies to an entry excluding one side of a rule rather
	// than the whole rule. It names the target that compares the side the entry
	// leaves in, holding the function value rather than the name, as
	// internal/format's coverage table does, so renaming the target breaks the
	// build instead of leaving the record pointing at a name that is gone. The
	// positive record rigCoverage cannot carry the claim, since
	// TestRigCoverageMatchesTheDraw checks its rows against the tagKinds pools
	// and those omit required on the collection kinds. The check over this field
	// runs one way. It reads what an entry claims, and cannot tell that an entry
	// claiming nothing excludes one side of its rule.
	covered func(*testing.F)
}

// rigExclusions is the reviewable record of everything this rig leaves out.
func rigExclusions() []rigExclusion {
	collections := []reflect.Type{
		reflect.TypeFor[[]string](), reflect.TypeFor[[]int8](),
		reflect.TypeFor[map[string]int](), reflect.TypeFor[[]byte](),
	}

	return []rigExclusion{
		{
			what:   "required on a slice, map, or byte slice holding an empty non-nil collection",
			reason: reasonRequiredCollectionEmptyFloor,
			rule:   "required", kinds: collections,
			covered: FuzzValidatorRequiredNullableShapes,
		},
		{
			what: "oneof on a bool or a float", reason: reasonOneOfKindPanic,
			rule:  "oneof",
			kinds: []reflect.Type{reflect.TypeFor[bool](), reflect.TypeFor[float64]()},
		},
		{
			what: "unique on a map", reason: reasonUniqueMapNoOp,
			rule: "unique", kinds: []reflect.Type{reflect.TypeFor[map[string]int]()},
		},
		{what: "the format tags", reason: reasonFormatDelegated, rule: "email"},
		{what: "the pattern tags", reason: reasonPatternDialect, rule: "alphanum"},
		{what: "the content tags", reason: reasonContentUnmodeled, rule: "base64"},
		{what: "a keys...endkeys block", reason: reasonKeysBlockUnmodeled, rule: "keys"},
		{what: "the cross-field validators", reason: reasonCrossFieldUnmodeled, rule: "eqfield"},
		{what: "the | OR operator", reason: reasonOrOperatorUnmodeled, rule: "|"},

		// The remaining entries are not rules the draw could spell. They record
		// where the harness itself compares less than the biconditional.
		{what: "required alongside eq=false on a bool", reason: reasonRequiredEqFalseConflict},
		{
			what:   "a numeric bound on a coerced field, drawn and checked as a rejection",
			reason: reasonCoercedNumericBounds,
		},
		{
			what:   "required on a nil collection behind a non-nil pointer",
			reason: reasonRequiredPointerNilCollection,
		},
		{
			what:   "required paired with omitempty on one field",
			reason: reasonRequiredOmitemptyDropped,
		},
		{what: "a field encoding/json dropped", reason: reasonOmitemptyDropsField},
		{what: "a field whose instance is null and carries no required", reason: reasonNullableValueRule},
	}
}

// rigCoverage is the positive half of the record: a rule the rig claims to
// compare, and the kinds whose pool must spell it. Without it
// TestRigExclusionsMatchTheDraw is one-directional, and a pool that quietly
// lost a rule would read as covered.
func rigCoverage() map[string][]reflect.Type {
	scalars := []reflect.Type{
		reflect.TypeFor[string](), reflect.TypeFor[int](),
		reflect.TypeFor[int8](), reflect.TypeFor[float64](),
	}

	return map[string][]reflect.Type{
		// The int8 and float64 pools are scalarNumber and its float clone, both
		// of which spell required and ne, so the record names them beside int.
		"required": {
			reflect.TypeFor[string](), reflect.TypeFor[int](), reflect.TypeFor[int8](),
			reflect.TypeFor[float64](), reflect.TypeFor[bool](),
		},
		"min": append(scalars, reflect.TypeFor[[]string](), reflect.TypeFor[map[string]int]()),
		"max": append(scalars, reflect.TypeFor[[]string](), reflect.TypeFor[map[string]int]()),
		"len": append(scalars, reflect.TypeFor[[]string](), reflect.TypeFor[map[string]int]()),
		"eq":  {reflect.TypeFor[string](), reflect.TypeFor[int](), reflect.TypeFor[bool]()},
		"ne": {
			reflect.TypeFor[string](), reflect.TypeFor[int](), reflect.TypeFor[int8](),
			reflect.TypeFor[float64](), reflect.TypeFor[bool](),
		},
		"oneof":  {reflect.TypeFor[string](), reflect.TypeFor[int]()},
		"unique": {reflect.TypeFor[[]string](), reflect.TypeFor[[]int8]()},
		"dive":   {reflect.TypeFor[[]string](), reflect.TypeFor[[]int8]()},
	}
}

// TestRigCoverageMatchesTheDraw pins that every rule the rig claims to compare
// is actually drawn for the kinds it names.
func TestRigCoverageMatchesTheDraw(t *testing.T) {
	t.Parallel()

	pools := make(map[reflect.Type][]string)
	for _, kind := range tagKinds() {
		pools[kind.typ] = kind.pool
	}

	for rule, kinds := range rigCoverage() {
		for _, typ := range kinds {
			assert.True(t, slices.ContainsFunc(pools[typ], func(s string) bool { return spells(s, rule) }),
				"the %s pool no longer spells %q, which the rig claims to cover", typ, rule)
		}
	}
}

// TestRigExclusionsMatchTheDraw pins the record against the draw pools. A rule
// the record excludes must be absent from the pools it names, every entry must
// carry a reason, and an entry naming a covering target must name the rule and
// kinds that target compares the other side of. Without this the record is
// prose that can drift away from what the draw does.
//
// The collection pools omit required, even though
// FuzzValidatorRequiredNullableShapes compares its nil side. The shape draw
// builds multi-field structs and the schema verdict is per object, so a sibling
// field's rejection would mask the field under test; the covering target puts
// each shape in a struct of its own for that reason.
func TestRigExclusionsMatchTheDraw(t *testing.T) {
	t.Parallel()

	pools := make(map[reflect.Type][]string)
	for _, kind := range tagKinds() {
		pools[kind.typ] = kind.pool
	}

	for _, ex := range rigExclusions() {
		assert.NotEmpty(t, ex.reason, "exclusion %q states no reason", ex.what)

		if ex.covered != nil {
			assert.NotEmpty(t, ex.rule, "exclusion %q names a covering target but no rule", ex.what)
			assert.NotEmpty(t, ex.kinds, "exclusion %q names a covering target but no kinds", ex.what)
		}

		if ex.rule == "" {
			continue
		}

		targets := ex.kinds
		if len(targets) == 0 {
			targets = make([]reflect.Type, 0, len(pools))
			for typ := range pools {
				targets = append(targets, typ)
			}
		}

		for _, typ := range targets {
			for _, spelling := range pools[typ] {
				assert.False(t, spells(spelling, ex.rule),
					"the %s pool spells %q, which %q excludes", typ, spelling, ex.what)
			}
		}
	}
}

// spells reports whether a pool entry uses the named rule, comparing the key
// rather than the whole spelling so an entry carrying a parameter still
// matches. It strips a dive prefix first, since a rule after dive is the same
// rule one level down.
func spells(spelling, rule string) bool {
	for part := range strings.SplitSeq(spelling, ",") {
		// Asking for dive itself is the one case that reads the prefix rather
		// than skipping it.
		if part == "dive" && rule != "dive" {
			continue
		}

		key, _, _ := strings.Cut(part, "=")
		if key == rule || (rule == "|" && strings.Contains(part, "|")) {
			return true
		}
	}

	return false
}
