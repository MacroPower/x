package differentialtest_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The reason constants name every case this rig does not compare, so widening
// the roster is a diff a reviewer can read rather than a doc comment they have
// to notice. Each states a divergence the interpreter takes by design, a shape
// go-playground itself cannot serve as a reference for, or a region where the
// agreement property is weaker than the biconditional.
const (
	// Go-playground applies required to a slice or map as a nil check, so an
	// empty but non-nil collection passes, while the interpreter models it as a
	// size floor and rejects the empty collection. A byte slice diverges the
	// same way with minLength standing in for minItems, since go-playground
	// reads the slice it is while the schema measures the base64 string it
	// becomes.
	reasonRequiredCollectionNilCheck = "required on a collection is a nil check in go-playground, a size floor in the schema"
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
	// RE2, which is not the engine go-playground matched with.
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
	// A JSON Schema keyword is type-conditional: minLength says nothing about a
	// null instance, so every value rule passes on one. Go-playground has no
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
}

// rigExclusions is the reviewable record of everything this rig leaves out.
func rigExclusions() []rigExclusion {
	collections := []reflect.Type{
		reflect.TypeFor[[]string](), reflect.TypeFor[[]int8](),
		reflect.TypeFor[map[string]int](), reflect.TypeFor[[]byte](),
	}

	return []rigExclusion{
		{
			what: "required on a slice, map, or byte slice", reason: reasonRequiredCollectionNilCheck,
			rule: "required", kinds: collections,
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
		{what: "a field encoding/json dropped", reason: reasonOmitemptyDropsField},
		{what: "a field whose instance is null", reason: reasonNullableValueRule},
	}
}

// TestRigExclusionsMatchTheDraw pins the record against the draw pools: a rule
// the record excludes must be absent from the pools it names, and every entry
// must carry a reason. Without this the record is prose that can drift away
// from what the draw does.
func TestRigExclusionsMatchTheDraw(t *testing.T) {
	t.Parallel()

	pools := make(map[reflect.Type][]string)
	for _, kind := range tagKinds() {
		pools[kind.typ] = kind.pool
	}

	for _, ex := range rigExclusions() {
		assert.NotEmpty(t, ex.reason, "exclusion %q states no reason", ex.what)

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
// matches. A dive prefix is stripped first, since a rule after dive is the same
// rule one level down.
func spells(spelling, rule string) bool {
	for part := range strings.SplitSeq(spelling, ",") {
		if part == "dive" {
			continue
		}

		key, _, _ := strings.Cut(part, "=")
		if key == rule || (rule == "|" && strings.Contains(part, "|")) {
			return true
		}
	}

	return false
}
