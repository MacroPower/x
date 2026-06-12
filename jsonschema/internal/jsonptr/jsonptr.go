// Package jsonptr implements RFC 6901 JSON Pointer reference-token escaping.
package jsonptr

import "strings"

// escaper and unescaper apply the RFC 6901 ~0/~1 transforms in a single
// pass. NewReplacer matches leftmost-longest without rescanning its own
// output, so unescaping "~1" before "~0" is order-correct.
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
