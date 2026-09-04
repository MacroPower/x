package normalize

import (
	"errors"
	"fmt"
	"slices"

	jsonv1 "encoding/json"
)

// errNoJSONForm marks a value whose own marshaler panicked, so DocumentValue
// reports it the way it reports a marshal error.
var errNoJSONForm = errors.New("value has no JSON form")

// DocumentValue returns the JSON-shaped value v denotes in the emitted schema
// document. The upstream Schema.MarshalJSON renders const, enum, and examples
// with [encoding/json] v1, where a nil map, slice, or pointer writes null, so
// DocumentValue marshals v the same way and decodes the bytes exactly through
// [DecodeJSONInstance]. A value [ValueChecked] accepts skips the round trip,
// since v1 renders it to the same shape, so every decoded document and every
// tag-authored value costs no marshal. The one accepted shape v1 renders
// differently is a nil []any or map[string]any, which it writes as null
// where the empty instance writes [] or {}, so a value holding one anywhere
// takes the round trip. DocumentValue reports ok=false where v has no JSON
// form (a func, a channel, a cyclic value) or its own marshaler panics, and
// recovers the panic so the caller sees only the flag.
func DocumentValue(v any) (any, bool) {
	if out, ok := ValueChecked(v); ok && !hasNilContainer(out) {
		return out, true
	}

	data, err := marshalV1(v)
	if err != nil {
		return nil, false
	}

	out, err := DecodeJSONInstance(data)
	if err != nil {
		return nil, false
	}

	return out, true
}

// marshalV1 marshals v with [encoding/json] v1, converting a panic in v's own
// marshaler into an error.
func marshalV1(v any) ([]byte, error) {
	var (
		data []byte
		err  error
	)

	func() {
		defer func() {
			if recover() != nil {
				data, err = nil, errNoJSONForm
			}
		}()

		data, err = jsonv1.Marshal(v)
	}()

	if err != nil {
		return nil, fmt.Errorf("marshal document value: %w", err)
	}

	return data, nil
}

// hasNilContainer reports whether an accepted JSON-shaped value holds a nil
// []any or map[string]any at any depth. [ValueChecked] has already refused a
// cyclic value, so the walk terminates.
func hasNilContainer(v any) bool {
	switch val := v.(type) {
	case []any:
		return val == nil || slices.ContainsFunc(val, hasNilContainer)

	case map[string]any:
		if val == nil {
			return true
		}

		for _, elem := range val {
			if hasNilContainer(elem) {
				return true
			}
		}

		return false
	}

	return false
}
