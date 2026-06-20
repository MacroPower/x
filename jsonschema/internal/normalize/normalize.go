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
// through unchanged and is rejected by [Accepted].
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
	normalized, _ := normalizeInstance(instance, map[[2]uintptr]bool{})

	return normalized
}

// normalizeInstance returns the normalized value and whether it differs from
// the input. The changed flag lets container normalization share unchanged
// children with the input instead of comparing interface values (which would
// panic on uncomparable types like maps and slices).
func normalizeInstance(instance any, onPath map[[2]uintptr]bool) (any, bool) {
	switch v := instance.(type) {
	case int:
		return json.Number(strconv.FormatInt(int64(v), 10)), true
	case int8:
		return json.Number(strconv.FormatInt(int64(v), 10)), true
	case int16:
		return json.Number(strconv.FormatInt(int64(v), 10)), true
	case int32:
		return json.Number(strconv.FormatInt(int64(v), 10)), true
	case int64:
		return json.Number(strconv.FormatInt(v, 10)), true
	case uint:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true
	case uint8:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true
	case uint16:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true
	case uint32:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true
	case uint64:
		return json.Number(strconv.FormatUint(v, 10)), true
	case uintptr:
		return json.Number(strconv.FormatUint(uint64(v), 10)), true
	case float32:
		return float64(v), true

	case map[string]any:
		return normalizeMap(v, onPath)
	case []any:
		return normalizeSlice(v, onPath)

	default:
		return instance, false
	}
}

// normalizeMap normalizes a map's values, returning the input map untouched
// when no value changes.
func normalizeMap(m map[string]any, onPath map[[2]uintptr]bool) (any, bool) {
	// Cycle guard: a self-referential instance (a map that contains itself,
	// directly or transitively) would otherwise recurse without bound and abort
	// the process with a stack overflow that recover cannot catch. Track the
	// containers on the current path and stop at a back-edge, leaving the value
	// unchanged. The key carries len alongside the pointer to stay uniform with
	// normalizeSlice, where the length distinguishes a reslice from a true cycle.
	key := [2]uintptr{reflect.ValueOf(m).Pointer(), uintptr(len(m))}
	if onPath[key] {
		return m, false
	}

	onPath[key] = true
	defer delete(onPath, key)

	var out map[string]any

	for k, val := range m {
		nv, changed := normalizeInstance(val, onPath)
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
		return m, false
	}

	return out, true
}

// normalizeSlice normalizes a slice's elements, returning the input slice
// untouched when no element changes.
func normalizeSlice(s []any, onPath map[[2]uintptr]bool) (any, bool) {
	// Cycle guard: see normalizeMap. A slice that contains itself would
	// otherwise recurse without bound and crash the process. The guard keys on
	// {data pointer, len} rather than the data pointer alone: a reslice such as
	// c[:1] shares c's data pointer but is a distinct, acyclic value, so keying
	// on the pointer alone would wrongly treat it as a back-edge and return it
	// un-normalized. A genuine self-reference re-enters with the same pointer
	// and length, so it is still caught.
	key := [2]uintptr{reflect.ValueOf(s).Pointer(), uintptr(len(s))}
	if onPath[key] {
		return s, false
	}

	onPath[key] = true
	defer delete(onPath, key)

	var out []any

	for i, val := range s {
		nv, changed := normalizeInstance(val, onPath)
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
		return s, false
	}

	return out, true
}
