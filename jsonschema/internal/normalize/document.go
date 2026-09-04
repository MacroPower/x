package normalize

import (
	jsonv1 "encoding/json"
)

// DocumentValue returns the JSON-shaped value v denotes in the emitted schema
// document. The upstream Schema.MarshalJSON renders const, enum, and examples
// with [encoding/json] v1, where a nil map, slice, or pointer writes null, so
// DocumentValue marshals v the same way and decodes the bytes exactly through
// [DecodeJSONInstance]. A value [ValueChecked] accepts skips the round trip,
// since v1 renders it to the same shape, so every decoded document and every
// tag-authored value costs no marshal. Four accepted shapes v1 renders
// differently, and a value holding one at any depth takes the round trip: a
// nil []any or map[string]any, which v1 writes as null where the empty
// instance writes [] or {}; a float32, which v1 formats as the 32-bit
// shortest decimal where ValueChecked widens the bits to float64 (0.1
// against 0.10000000149011612); an empty [jsonv1.Number], which v1 writes as
// 0; and a string or member name holding invalid UTF-8, which v1 writes with
// U+FFFD in place of each bad byte. DocumentValue reports ok=false where v
// has no JSON form (a func, a channel, a cyclic value) or its own marshaler
// panics, and recovers the panic so the caller sees only the flag.
func DocumentValue(v any) (any, bool) {
	if out, ok, render := documentChecked(v); ok && !render {
		return out, true
	}

	data, ok := marshalV1(v)
	if !ok {
		return nil, false
	}

	out, err := DecodeJSONInstance(data)
	if err != nil {
		return nil, false
	}

	return out, true
}

// marshalV1 marshals v with [encoding/json] v1 and reports ok=false where the
// marshal returns an error or v's own marshaler panics. A recovered panic
// leaves the unnamed results at their zero values, nil and false, which is
// the refusal the caller expects.
func marshalV1(v any) ([]byte, bool) {
	defer func() {
		if recover() != nil {
			return
		}
	}()

	data, err := jsonv1.Marshal(v)

	return data, err == nil
}
