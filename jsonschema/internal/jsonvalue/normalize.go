package jsonvalue

import (
	"maps"
	"reflect"

	jsonv1 "encoding/json"
)

// Normalize converts a Go value into the legacy JSON-shaped any form the
// public Normalize entry point returns, applying [FromGo]'s conversions in
// place of the Value tree: integer widths become [jsonv1.Number], float32
// widens to float64, and map[string]any and []any are normalized
// recursively. Everything else passes through unchanged.
//
// Containers are copied only when normalization changes something inside
// them, so an already JSON-shaped value is returned as is and the input is
// never mutated. A self-referential instance (a map or slice that contains
// itself) is not descended past the cycle, so Normalize terminates instead of
// overflowing the stack; the back-edge in the result keeps pointing at the
// original container.
func Normalize(instance any) any {
	out, _ := normalizeNode(instance, map[[2]uintptr]bool{})

	return out
}

// normalizeNode returns the normalized value and whether it differs from the
// input. The changed flag lets container normalization share unchanged
// children with the input instead of comparing interface values (which would
// panic on uncomparable types like maps and slices).
func normalizeNode(instance any, onPath map[[2]uintptr]bool) (any, bool) {
	switch v := instance.(type) {
	case nil, bool, float64, string, jsonv1.Number:
		return instance, false
	case float32:
		return float64(v), true
	case map[string]any:
		return normalizeMap(v, onPath)
	case []any:
		return normalizeSlice(v, onPath)
	default:
		if lit, ok := intLiteral(instance); ok {
			return jsonv1.Number(lit), true
		}

		return instance, false
	}
}

// normalizeMap normalizes a map's values, returning the input map untouched
// when no value changes. The cycle guard shares [walk.object]'s key shape and
// stops at a back-edge, leaving the value unchanged.
func normalizeMap(m map[string]any, onPath map[[2]uintptr]bool) (any, bool) {
	key := [2]uintptr{reflect.ValueOf(m).Pointer(), uintptr(len(m))}
	if onPath[key] {
		return m, false
	}

	onPath[key] = true
	defer delete(onPath, key)

	var out map[string]any

	for k, val := range m {
		nv, changed := normalizeNode(val, onPath)
		if !changed {
			// The clone below snapshots every key, including ones not yet
			// visited, so it already holds this unchanged value.
			continue
		}

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
// untouched when no element changes, on the same terms as normalizeMap.
func normalizeSlice(s []any, onPath map[[2]uintptr]bool) (any, bool) {
	key := [2]uintptr{reflect.ValueOf(s).Pointer(), uintptr(len(s))}
	if onPath[key] {
		return s, false
	}

	onPath[key] = true
	defer delete(onPath, key)

	var out []any

	for i, val := range s {
		nv, changed := normalizeNode(val, onPath)
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
