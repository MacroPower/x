package normalize

import (
	"encoding/json/v2"
	"unicode/utf8"

	jsonv1 "encoding/json"
)

// maxExactDepth caps [ExactValue]'s container recursion at the nesting depth
// [encoding/json/jsontext] enforces, so a cyclic or absurdly deep value fails
// the copy exactly where the marshal round trip it mirrors would fail, instead
// of overflowing the stack.
const maxExactDepth = 10000

// ExactValue deep-copies a JSON-shaped value with numbers as exact
// [jsonv1.Number] literals, reporting ok=false where the value has no JSON
// form. It is the direct equivalent of a [json.Marshal] + [DecodeJSONInstance]
// round trip, without re-encoding the containers: scalars keep the literal
// that round trip would produce (a float leaf is rendered by the same
// encoder, so the shortest-decimal literal is byte-identical, including
// float32's 32-bit form), containers are rebuilt fresh so the copy shares no
// memory with the source, and a value outside the JSON shape (a struct, a
// raw jsontext.Value, a [jsonv1.RawMessage]) takes the round trip for its
// own subtree, failing exactly where its marshal would.
func ExactValue(v any) (any, bool) {
	return exactValue(v, 0)
}

// exactValue implements [ExactValue] with the container depth threaded for
// the [maxExactDepth] cap.
func exactValue(v any, depth int) (any, bool) {
	// Go integer widths format exactly at any magnitude, through the switch
	// normalizeInstance shares.
	if n, ok := intNumber(v); ok {
		return n, true
	}

	switch val := v.(type) {
	case nil, bool:
		return v, true

	case string:
		// The mirrored round trip rejects invalid UTF-8 in strings
		// (RFC 7493), so the copy must too.
		if !utf8.ValidString(val) {
			return nil, false
		}

		return v, true

	// The literal-carrying scalars render through the same v2 encoder the
	// mirrored round trip uses, so the copied literal is byte-identical.
	case jsonv1.Number, float64, float32:
		return exactEncodedNumber(v)

	case map[string]any:
		if depth >= maxExactDepth {
			return nil, false
		}

		out := make(map[string]any, len(val))

		for k, member := range val {
			// The mirrored round trip rejects an invalid UTF-8 member name.
			if !utf8.ValidString(k) {
				return nil, false
			}

			cp, ok := exactValue(member, depth+1)
			if !ok {
				return nil, false
			}

			out[k] = cp
		}

		return out, true

	case []any:
		if depth >= maxExactDepth {
			return nil, false
		}

		out := make([]any, len(val))

		for i, elem := range val {
			cp, ok := exactValue(elem, depth+1)
			if !ok {
				return nil, false
			}

			out[i] = cp
		}

		return out, true

	default:
		return exactRoundTrip(v)
	}
}

// exactEncodedNumber renders a literal-carrying numeric scalar through the v2
// encoder and wraps the bytes as the [jsonv1.Number] literal the mirrored
// round trip would decode. The encoder also supplies the refusals, since an
// invalid [jsonv1.Number] literal, NaN, and the infinities have no JSON form.
func exactEncodedNumber(v any) (any, bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}

	return jsonv1.Number(data), true
}

// exactRoundTrip copies a value outside the JSON shape by the marshal + exact
// decode [ExactValue] otherwise avoids, reporting ok=false where the value
// has no JSON form.
func exactRoundTrip(v any) (any, bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}

	out, err := DecodeJSONInstance(data)
	if err != nil {
		return nil, false
	}

	return out, true
}
