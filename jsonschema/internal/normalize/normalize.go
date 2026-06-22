// Package normalize converts a Go value into the JSON-shaped value the
// validator works with: signed and unsigned integers of every width become
// [encoding/json.Number] (preserving the exact value where a float64 conversion
// would round), float32 widens to float64, and map[string]any and []any are
// coerced recursively. Containers are copied only when normalization changes
// something inside them, so an already JSON-shaped value is returned untouched
// and the input is never mutated. A `{pointer, len}` cycle guard stops the
// recursion at a self-referential map or slice while still normalizing a reslice
// that merely shares a data pointer.
package normalize

import (
	"encoding/json"
	"maps"
	"reflect"
	"strconv"
)

// Value converts a Go value into the JSON-compatible shape the validator works
// with, so instances decoded from non-encoding/json sources (YAML, TOML, a
// hand-built map[string]any) validate without a manual conversion pass:
//
//   - Signed and unsigned integer values become [encoding/json.Number],
//     preserving the exact value at any magnitude (a float64 conversion would
//     round above 2^53).
//   - float32 widens to float64.
//   - map[string]any and []any are normalized recursively.
//   - nil, bool, string, float64 (including NaN and the infinities, which JSON
//     marshaling would reject), and [encoding/json.Number] pass through
//     unchanged.
//
// Anything else (structs, named numeric types, map[any]any) also passes
// through unchanged; [ValueChecked] reports such a leaf as not accepted.
//
// Containers are copied only when normalization changes something inside them;
// an already JSON-shaped value is returned as-is, and the input is never
// mutated.
//
// A self-referential instance (a map or slice that contains itself) is not
// descended past the cycle, so Value terminates instead of overflowing the
// stack. Validating such an instance against a recursive schema may still
// recurse without bound, so callers building cyclic instances by hand should
// avoid recursive schemas.
func Value(instance any) any {
	normalized, _, _ := normalizeInstance(instance, map[[2]uintptr]bool{})

	return normalized
}

// ValueChecked normalizes instance like [Value] and additionally reports
// whether every leaf is, after normalization, a JSON-shaped value the
// validation walk accepts: map[string]any, []any, string, bool, nil, float64,
// [encoding/json.Number], or a Go numeric kind Value converts. It folds
// normalization and the acceptance check into a single tree walk for the
// validation funnel, which would otherwise normalize the instance and then
// re-walk the whole structure a second time to check acceptance.
func ValueChecked(instance any) (any, bool) {
	normalized, _, accepted := normalizeInstance(instance, map[[2]uintptr]bool{})

	return normalized, accepted
}

// normalizeInstance returns the normalized value, whether it differs from the
// input, and whether it (and every nested leaf) is an accepted JSON-shaped
// value. The changed flag lets container normalization share unchanged children
// with the input instead of comparing interface values (which would panic on
// uncomparable types like maps and slices). The accepted flag carries the
// acceptance check through the same walk, so the validation funnel needs only
// one traversal.
func normalizeInstance(instance any, onPath map[[2]uintptr]bool) (any, bool, bool) {
	switch v := instance.(type) {
	// JSON-shaped scalar leaves pass through unchanged and are accepted.
	case nil, bool, string, float64, json.Number:
		return instance, false, true

	// Go integer and float widths convert to the JSON shape; the result is an
	// accepted leaf.
	case int:
		return json.Number(strconv.FormatInt(int64(v), 10)), true, true
	case int8:
		return json.Number(strconv.FormatInt(int64(v), 10)), true, true
	case int16:
		return json.Number(strconv.FormatInt(int64(v), 10)), true, true
	case int32:
		return json.Number(strconv.FormatInt(int64(v), 10)), true, true
	case int64:
		return json.Number(strconv.FormatInt(v, 10)), true, true
	case uint:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true, true
	case uint8:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true, true
	case uint16:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true, true
	case uint32:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true, true
	case uint64:
		return json.Number(strconv.FormatUint(v, 10)), true, true
	case uintptr:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true, true
	case float32:
		return float64(v), true, true

	case map[string]any:
		return normalizeMap(v, onPath)
	case []any:
		return normalizeSlice(v, onPath)

	default:
		// Not a JSON-shaped type (struct, named numeric, map[any]any, channel,
		// ...): pass through unchanged and report it unaccepted so the funnel
		// rejects it rather than mis-validating deeper in the walk.
		return instance, false, false
	}
}

// normalizeMap normalizes a map's values, returning the input map untouched
// when no value changes.
func normalizeMap(m map[string]any, onPath map[[2]uintptr]bool) (any, bool, bool) {
	// Cycle guard: a self-referential instance (a map that contains itself,
	// directly or transitively) would otherwise recurse without bound and abort
	// the process with a stack overflow that recover cannot catch. Track the
	// containers on the current path and stop at a back-edge, leaving the value
	// unchanged. The key reuses normalizeSlice's {pointer, len} shape so both
	// helpers share one onPath map; for a map the pointer alone identifies the
	// container (a map cannot be resliced into a distinct value sharing its
	// pointer), so len never changes the decision here and is carried only for
	// that uniformity. A back-edge re-enters a container already on the path, so
	// it is accepted.
	key := [2]uintptr{reflect.ValueOf(m).Pointer(), uintptr(len(m))}
	if onPath[key] {
		return m, false, true
	}

	onPath[key] = true
	defer delete(onPath, key)

	var out map[string]any

	allAccepted := true

	for k, val := range m {
		nv, changed, ok := normalizeInstance(val, onPath)
		if !ok {
			allAccepted = false
		}

		if !changed {
			// The clone below snapshots every key, including ones not yet
			// visited, so it already holds this unchanged value; no write
			// needed. (normalizeSlice differs: it copies only the s[:i] prefix
			// and so must fill its unchanged tail.)
			continue
		}

		// First change: clone the input (every entry is carried over by the
		// clone), then overwrite only the entries that change.
		if out == nil {
			out = maps.Clone(m)
		}

		out[k] = nv
	}

	if out == nil {
		return m, false, allAccepted
	}

	return out, true, allAccepted
}

// normalizeSlice normalizes a slice's elements, returning the input slice
// untouched when no element changes.
func normalizeSlice(s []any, onPath map[[2]uintptr]bool) (any, bool, bool) {
	// Cycle guard: see normalizeMap. A slice that contains itself would
	// otherwise recurse without bound and crash the process. The guard keys on
	// {data pointer, len} rather than the data pointer alone: a reslice such as
	// c[:1] shares c's data pointer but is a distinct, acyclic value, so keying
	// on the pointer alone would wrongly treat it as a back-edge and return it
	// un-normalized. A genuine self-reference re-enters with the same pointer
	// and length, so it is still caught. A back-edge is accepted.
	key := [2]uintptr{reflect.ValueOf(s).Pointer(), uintptr(len(s))}
	if onPath[key] {
		return s, false, true
	}

	onPath[key] = true
	defer delete(onPath, key)

	var out []any

	allAccepted := true

	for i, val := range s {
		nv, changed, ok := normalizeInstance(val, onPath)
		if !ok {
			allAccepted = false
		}

		if !changed {
			if out != nil {
				out[i] = val
			}

			continue
		}

		if out == nil {
			out = make([]any, len(s))
			copy(out, s[:i])
		}

		out[i] = nv
	}

	if out == nil {
		return s, false, allAccepted
	}

	return out, true, allAccepted
}
