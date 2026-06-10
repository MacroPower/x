package jsonschema

import (
	"encoding/json"
	"maps"
	"strconv"
)

// Normalize converts a Go value into the JSON-compatible shape the validator
// works with, so instances decoded from non-encoding/json sources (YAML, TOML,
// a hand-built map[string]any) validate without a manual conversion pass:
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
// through unchanged and is rejected by [Validate]'s instance-type check.
//
// Containers are copied only when normalization changes something inside them;
// an already JSON-shaped value is returned as-is, and the input is never
// mutated. [Validator.Validate] and [Validate] normalize their instance
// automatically; Normalize is exported so a caller can pre-normalize a value
// once and reuse it across many validations.
func Normalize(instance any) any {
	normalized, _ := normalizeInstance(instance)

	return normalized
}

// normalizeInstance returns the normalized value and whether it differs from
// the input. The changed flag lets container normalization share unchanged
// children with the input instead of comparing interface values (which would
// panic on uncomparable types like maps and slices).
func normalizeInstance(instance any) (any, bool) {
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
		return normalizeMap(v)
	case []any:
		return normalizeSlice(v)

	default:
		return instance, false
	}
}

// normalizeMap normalizes a map's values, returning the input map untouched
// when no value changes.
func normalizeMap(m map[string]any) (any, bool) {
	var out map[string]any

	for k, val := range m {
		nv, changed := normalizeInstance(val)
		if !changed {
			if out != nil {
				out[k] = val
			}

			continue
		}

		// First change: clone the input (every entry visited so far is
		// unchanged), then keep filling the clone.
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
func normalizeSlice(s []any) (any, bool) {
	var out []any

	for i, val := range s {
		nv, changed := normalizeInstance(val)
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
