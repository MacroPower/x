// Package jsonptr implements RFC 6901 JSON Pointer reference-token escaping,
// array-index parsing, and navigation of a generated schema by JSON Pointer.
package jsonptr

import (
	"net/url"
	"strconv"
	"strings"
)

// escaper and unescaper apply the RFC 6901 ~0/~1 transforms in a single pass.
// A [strings.NewReplacer] scans left to right and never rescans its own output,
// so the "/" written while unescaping "~1" is not re-examined and cannot
// combine with a following "0" into a spurious "~0". Because "~0" and "~1"
// differ at their second byte they never both match at one position, so the
// order of the two pairs is irrelevant; the single-pass, no-rescan behavior is
// what makes the round trip correct.
var (
	escaper   = strings.NewReplacer("~", "~0", "/", "~1")
	unescaper = strings.NewReplacer("~1", "/", "~0", "~")

	// The pctSlashUnescaper folds both spellings of a percent-encoded pointer
	// separator ("%2F" and its lowercase form) to '/' in one left-to-right pass.
	// The two patterns differ at their last byte so they never both match at one
	// position, and the written '/' can never form a new "%2F"/"%2f", so a single
	// pass equals two sequential [strings.ReplaceAll] calls.
	pctSlashUnescaper = strings.NewReplacer("%2F", "/", "%2f", "/")
)

// Escape escapes a string per RFC 6901.
func Escape(s string) string {
	return escaper.Replace(s)
}

// Unescape unescapes a JSON Pointer segment per RFC 6901.
func Unescape(s string) string {
	return unescaper.Replace(s)
}

// SafeToken restricts a token to the conservative unreserved set [A-Za-z0-9._-]
// so the generated $ref resolves in external tools as both an RFC 6901 pointer
// token and an RFC 3986 URI fragment. Generic type names embed characters that
// are invalid in one or both: brackets, commas, spaces, and braces from
// type-argument lists; the slash and tilde (the pointer separator and escape);
// and quotes, asterisks, and the like from anonymous struct tags and pointer
// arguments (a reflect name such as Box[struct { A int <tag> }] carries spaces,
// braces, and tag quotes). Every rune outside the unreserved set is mapped to
// '_' so the generated $ref resolves in external tools, not only the calling
// package's own resolver.
func SafeToken(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

// FragmentSegments decodes the path of a JSON Pointer URI fragment into its RFC
// 6901 reference tokens. It returns the decoded tokens and true, or nil and
// false when fragment does not begin with the root separator.
//
// Encoded reports whether fragment is still percent-encoded (the caller had a
// RawFragment). When set, each token is percent-decoded after the split; when
// clear, [net/url.Parse] has already decoded the fragment and a second decode
// would corrupt a token that legitimately contains '%', so only the ~0/~1
// unescape is applied.
func FragmentSegments(fragment string, encoded bool) ([]string, bool) {
	// Strip the leading '/' root separator. The caller passes only fragments
	// whose decoded form starts with it, but in the still-encoded form that
	// separator may be a literal '/' or a percent-escaped %2F. Dropping the
	// first byte blindly would mangle %2Ffoo into the "2Ffoo" segment, so match
	// either spelling.
	var path string

	switch {
	case strings.HasPrefix(fragment, "/"):
		path = fragment[1:]
	case encoded && len(fragment) >= 3 && strings.EqualFold(fragment[:3], "%2f"):
		path = fragment[3:]
	default:
		return nil, false
	}

	// A %2F is the percent-encoding of the pointer separator '/', so normalize
	// every occurrence to '/' before splitting, not just the leading one. Per
	// RFC 6901 a literal '/' inside a member name is escaped as ~1 (decoded
	// below), never as %2F, so this cannot split a member name; a fully
	// percent-escaped pointer such as "%2Ffoo%2Fbar" therefore resolves like
	// "/foo/bar".
	if encoded {
		path = pctSlashUnescaper.Replace(path)
	}

	segments := strings.Split(path, "/")

	// When the fragment was still percent-encoded (the caller had a
	// RawFragment), percent-decode each segment after the split. When
	// [net/url.Parse] already decoded the fragment (RawFragment empty), a second
	// decode would corrupt a name that legitimately contains '%', so only the
	// ~0/~1 unescape is applied.
	for i, seg := range segments {
		if encoded {
			decoded, err := url.PathUnescape(seg)
			if err == nil {
				seg = decoded
			}

			// On an invalid percent-escape the segment is left as-is; resolution
			// then simply does not match.
		}

		segments[i] = Unescape(seg)
	}

	return segments, true
}

// ParseArrayIndex parses a JSON Pointer reference token as an RFC 6901 array
// index. The grammar admits only "0" or a nonzero leading digit followed by
// digits, so non-canonical forms such as "01", "+1", or "-0" are rejected. It
// returns the parsed index and true on success, or false otherwise.
func ParseArrayIndex(seg string) (int, bool) {
	if seg == "" {
		return 0, false
	}

	if seg != "0" && seg[0] == '0' {
		return 0, false
	}

	for i := range len(seg) {
		if seg[i] < '0' || seg[i] > '9' {
			return 0, false
		}
	}

	idx, err := strconv.Atoi(seg)
	if err != nil {
		return 0, false
	}

	return idx, true
}

// SegmentsKey joins decoded JSON Pointer segments into a single string that is
// injective regardless of the segment contents, so it can key a cache by a
// segment list. A fixed separator byte cannot do this: a JSON member name may
// legitimately contain any byte (NUL included), so joining on one would let
// ["a\x00b"] collide with ["a", "b"]. Length-prefixing each segment keeps the
// encoding uniquely decodable, so distinct segment lists never share a key.
func SegmentsKey(segments []string) string {
	var b strings.Builder

	for _, seg := range segments {
		b.WriteString(strconv.Itoa(len(seg)))
		b.WriteByte(':')
		b.WriteString(seg)
	}

	return b.String()
}
