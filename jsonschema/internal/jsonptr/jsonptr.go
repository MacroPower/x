// Package jsonptr implements RFC 6901 JSON Pointer reference-token escaping,
// array-index parsing, and navigation of a generated schema by JSON Pointer.
package jsonptr

import (
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
