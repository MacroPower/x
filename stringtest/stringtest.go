package stringtest

import "strings"

// JoinLF joins multiple strings with LF line endings.
// Use this to construct expected test output with explicit line endings.
//
// Example:
//
//	want := stringtest.JoinLF(
//		"line1",
//		"line2",
//		"line3",
//	) // -> "line1\nline2\nline3"
func JoinLF(ss ...string) string {
	var sb strings.Builder
	for i, s := range ss {
		if i > 0 {
			sb.WriteByte('\n')
		}

		sb.WriteString(s)
	}

	return sb.String()
}

// JoinCRLF joins multiple strings with CRLF line endings.
// Use this to construct expected test output with explicit line endings on
// Windows.
//
// Example:
//
//	want := stringtest.JoinCRLF(
//		"line1",
//		"line2",
//		"line3",
//	) // -> "line1\r\nline2\r\nline3"
func JoinCRLF(ss ...string) string {
	var sb strings.Builder
	for i, s := range ss {
		if i > 0 {
			sb.WriteByte('\r')
			sb.WriteByte('\n')
		}

		sb.WriteString(s)
	}

	return sb.String()
}
