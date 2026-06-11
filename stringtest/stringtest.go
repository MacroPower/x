package stringtest

import "strings"

// JoinLF joins strings with LF (\n) line endings.
//
// Use this to construct expected test output with explicit line endings.
// See also [JoinCRLF] for Windows-style line endings.
// See also [LinesLF] for newline-terminated output.
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

// JoinCRLF joins strings with CRLF (\r\n) line endings.
//
// Use this to construct expected test output with explicit Windows-style line
// endings. See also [JoinLF] for Unix-style line endings.
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

// TrimLineEnds trims trailing spaces and tabs from every line of s,
// preserving line structure and any final newline. CRLF line endings are
// preserved: whitespace before a trailing \r is trimmed and the \r kept.
//
// Use this to normalize rendered output before comparing it against
// expected strings built with [JoinLF]; terminal renderers such as
// lipgloss pad lines with trailing spaces to the render width.
func TrimLineEnds(s string) string {
	if s == "" {
		return ""
	}

	lines := strings.Split(s, "\n")

	for i, line := range lines {
		if strings.HasSuffix(line, "\r") {
			lines[i] = strings.TrimRight(line[:len(line)-1], " \t") + "\r"
		} else {
			lines[i] = strings.TrimRight(line, " \t")
		}
	}

	return strings.Join(lines, "\n")
}

// LinesLF joins strings as newline-terminated lines using LF (\n) line endings.
//
// Each argument is treated as one complete line; every line, including the
// last, is terminated with "\n". LinesLF("a", "b") == "a\nb\n". LinesLF()
// == "".
//
// This differs from [JoinLF], which uses "\n" as a separator and does not
// terminate the final element. See also [JoinLF] when the output does not
// end in a newline. See also [LinesCRLF] for Windows-style line endings.
//
// Example:
//
//	want := stringtest.LinesLF(
//		"line1",
//		"line2",
//		"line3",
//	) // -> "line1\nline2\nline3\n"
func LinesLF(ss ...string) string {
	if len(ss) == 0 {
		return ""
	}

	var sb strings.Builder

	for _, s := range ss {
		sb.WriteString(s)
		sb.WriteByte('\n')
	}

	return sb.String()
}

// LinesCRLF joins strings as newline-terminated lines using CRLF (\r\n) line endings.
//
// Each argument is treated as one complete line; every line, including the
// last, is terminated with "\r\n". LinesCRLF("a", "b") == "a\r\nb\r\n".
// LinesCRLF() == "".
//
// This differs from [JoinCRLF], which uses "\r\n" as a separator and does
// not terminate the final element. See also [JoinCRLF] when the output does
// not end in a newline. See also [LinesLF] for Unix-style line endings.
//
// Example:
//
//	want := stringtest.LinesCRLF(
//		"line1",
//		"line2",
//		"line3",
//	) // -> "line1\r\nline2\r\nline3\r\n"
func LinesCRLF(ss ...string) string {
	if len(ss) == 0 {
		return ""
	}

	var sb strings.Builder

	for _, s := range ss {
		sb.WriteString(s)
		sb.WriteByte('\r')
		sb.WriteByte('\n')
	}

	return sb.String()
}

// Margin normalizes a test string using margin markers, preserving
// significant leading whitespace. Each line is written with a '|' marker;
// everything before and including the first '|' (which must be preceded
// only by spaces and tabs) is removed, and everything after it is kept
// verbatim. Lines without a marker are left unchanged.
//
// At most one leading newline is stripped (so the block can start on the
// line after the opening backtick), and a final whitespace-only line
// without a marker is dropped along with the newline that precedes it (so
// the closing backtick can be indented and the result carries no trailing
// newline). Unlike [Input], trailing whitespace on marked lines is
// preserved. Pair with [LinesLF] semantics by appending "\n" when a
// newline-terminated want is needed.
//
// Example:
//
//	want := stringtest.Margin(`
//	    |   1 | first line with leading spaces
//	    |   2 | second line
//	    |   3 |
//	`)
//	// -> "   1 | first line with leading spaces\n   2 | second line\n   3 |"
func Margin(s string) string {
	s = strings.TrimPrefix(s, "\n")

	lines := strings.Split(s, "\n")

	// Drop the final line if it is whitespace-only and contains no '|'.
	if n := len(lines); n > 0 {
		last := lines[n-1]
		if strings.TrimSpace(last) == "" && !strings.Contains(last, "|") {
			lines = lines[:n-1]
		}
	}

	for i, line := range lines {
		// Find the first non-space/non-tab character.
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "|") {
			lines[i] = trimmed[1:]
		}

		// Otherwise leave the line unchanged.
	}

	return strings.Join(lines, "\n")
}

// Input is a helper to normalize test input strings.
//
// It dedents the string by removing the common leading whitespace from all
// lines, allowing test inputs to be indented for readability while producing
// clean output.
//
// At most one leading newline and one trailing newline are stripped.
func Input(s string) string {
	// Strip at most one leading newline (allows backtick strings to start on next line).
	s = strings.TrimPrefix(s, "\n")

	// Strip trailing spaces/tabs (allows closing backtick to be indented).
	s = strings.TrimRight(s, " \t")

	// Strip at most one trailing newline.
	s = strings.TrimSuffix(s, "\n")

	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return ""
	}

	// Find minimum indentation across non-empty lines.
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue // Skip empty/whitespace-only lines.
		}

		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}

	if minIndent <= 0 {
		return strings.Join(lines, "\n")
	}

	// Remove common indentation from all lines.
	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}

	return strings.Join(lines, "\n")
}
