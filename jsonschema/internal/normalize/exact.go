package normalize

import (
	"encoding/json/v2"
)

// ExactValue deep-copies a JSON-shaped value with numbers as exact
// [encoding/json.Number] literals, reporting ok=false where the value has no
// JSON form. It is a [json.Marshal] + [DecodeJSONInstance] round trip:
// scalars keep the literal the v2 encoder renders (a float leaf takes the
// shortest-decimal form, including float32's 32-bit one), containers come
// back rebuilt so the copy shares no memory with the source, and the
// marshal supplies the refusals -- invalid UTF-8 in strings and member
// names (RFC 7493), NaN and the infinities, a cyclic or absurdly deep
// value, anything else without a JSON form.
func ExactValue(v any) (any, bool) {
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
