// Package content asserts the JSON Schema contentEncoding and contentMediaType
// keywords for a string instance.
//
// Only base64 encoding and the application/json media type carry an assertion;
// an unrecognized encoding leaves both keywords as annotations rather than
// running the media-type check against still-encoded text. The pure
// decode-and-classify core lives here so its RFC edge cases -- base64
// round-tripping and the [mime.ParseMediaType]-with-hand-folded fallback for
// forms like "Application/JSON" and "application/json; charset=utf-8" -- are
// testable without a full Compile-and-validate. The validator keeps the
// vocabulary gating and the error construction.
package content

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"strings"
)

// Base64 is the only contentEncoding this package decodes, and the value the
// generator emits for a []byte. It is the single source of truth shared by the
// generation and validation halves.
const Base64 = "base64"

// MediaTypeIsJSON reports whether a contentMediaType denotes application/json.
// Per RFC 2045 the type/subtype is case-insensitive and any parameters (for
// example "; charset=utf-8") are not part of it, so "Application/JSON" and
// "application/json; charset=utf-8" both match.
func MediaTypeIsJSON(mediaType string) bool {
	parsed, _, err := mime.ParseMediaType(mediaType)
	if err == nil {
		// ParseMediaType lowercases the type/subtype and strips parameters.
		return parsed == "application/json"
	}

	// ParseMediaType rejects some malformed-but-recognizable values, so fall
	// back to stripping parameters and folding case by hand.
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}

	return strings.EqualFold(strings.TrimSpace(mediaType), "application/json")
}

// Assert checks contentEncoding and contentMediaType for the string instance
// str. It returns the JSON Schema keyword that failed, plus the decode error
// for a base64 failure so the caller can build the message:
//
//   - ("contentEncoding", err) when encoding is [Base64] and str is not valid
//     base64;
//   - ("contentMediaType", nil) when the decoded form is a JSON media type but
//     not valid JSON;
//   - ("", nil) when the content passes, or when the encoding is unrecognized
//     and so cannot be decoded for the media-type check (both keywords stay
//     annotations).
func Assert(encoding, mediaType, str string) (string, error) {
	decoded := []byte(str)
	decodedKnown := true

	switch encoding {
	case "":
		// No encoding: the instance string is the content itself.
	case Base64:
		b, err := base64.StdEncoding.DecodeString(str)
		if err != nil {
			return "contentEncoding", err
		}

		decoded = b

	default:
		// An unrecognized encoding cannot be decoded, so the media type cannot
		// be asserted against the decoded form; both keywords remain annotations
		// rather than running the assertion on still-encoded text.
		decodedKnown = false
	}

	if decodedKnown && MediaTypeIsJSON(mediaType) && !json.Valid(decoded) {
		return "contentMediaType", nil
	}

	return "", nil
}
