// Package jsonptr implements RFC 6901 JSON Pointer reference-token escaping.
package jsonptr

import "strings"

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
