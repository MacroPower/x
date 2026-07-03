// Package yamldoc preprocesses raw YAML document bytes -- line-ending
// normalization, empty-document stripping, and per-document splitting --
// ahead of parsing.
package yamldoc

import (
	"bytes"
	"regexp"
	"strings"
)

// blockScalarHeader matches a line whose value position holds only a block
// scalar indicator with optional chomping/indentation modifiers and an
// optional trailing comment: "key: |", "- >-", "key: |2+ # c". The indicator
// must be the whole value, so a plain scalar that merely contains a pipe
// ("cmd: foo | bar") does not count.
var blockScalarHeader = regexp.MustCompile(`[:\-][ \t]+[|>]\d*[+-]?\d*[ \t]*(?:#.*)?$`)

// MaskBlockScalars splits content into lines with the interior of block
// scalars (literal "|" and folded ">" values) blanked out. Line-oriented
// annotation scans (bitnami's ## @param, norwoodj's old-style descriptions)
// iterate these lines instead of a raw split, so string DATA inside a block
// scalar -- which may look exactly like an annotation comment -- can never
// register an annotation and attach a wrong type or description to a real
// key, producing a schema the source file itself fails. Line count and
// order are preserved; only interior lines become empty.
//
// Detection is textual: a non-comment line whose value position holds only a
// block scalar indicator opens a scalar, and every following line that is
// blank or indented past the header line belongs to it. Block scalar content
// must be indented past the line that introduces it, so the first line back
// at or under the header's indentation ends the scalar.
func MaskBlockScalars(content []byte) []string {
	lines := strings.Split(string(content), "\n")

	inScalar, headerIndent := false, 0

	for i, line := range lines {
		if inScalar {
			if IsBlank([]byte(line)) || indentOf(line) > headerIndent {
				lines[i] = ""

				continue
			}

			inScalar = false
		}

		// A comment line ending in an indicator ("# usage: |") opens nothing;
		// without the guard it would swallow indented annotation lines below.
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "#") && blockScalarHeader.MatchString(line) {
			inScalar, headerIndent = true, indentOf(line)
		}
	}

	return lines
}

// indentOf counts a line's leading spaces. YAML forbids tabs in indentation,
// so spaces alone determine nesting.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// DropEmptyDocuments removes empty documents from a multi-document YAML
// stream by blanking each bare "---" separator that is followed, across
// blank lines only, by another document start line -- either another bare
// separator or a "---" marker carrying same-line content ("--- {b: 2}").
// The goccy/go-yaml parser stops emitting documents after an empty one
// ("a: 1\n---\n\n---\nb: 2" parses as two documents, losing b entirely),
// and empty documents contribute nothing to the union (a nil document body
// is skipped), so removing them up front preserves semantics while keeping
// later documents in the stream. The dropped separator is replaced with a
// blank line rather than deleted, so every later line keeps its physical
// line number and parser positions -- error messages, comment attribution --
// still point at the user's actual file.
func DropEmptyDocuments(input []byte) []byte {
	// A bare separator line requires the "---" substring, so its absence means
	// there are no documents to collapse and the split/join would be a no-op.
	if !bytes.Contains(input, []byte("---")) {
		return input
	}

	lines := bytes.Split(input, []byte("\n"))

	out := make([][]byte, 0, len(lines))

	for i := range lines {
		if !isSeparatorLine(lines[i]) {
			out = append(out, lines[i])

			continue
		}

		// Look ahead past blank lines; a following document start line means
		// this separator opens an empty document, so blank it out. The blank
		// run and the start line keep their own lines: a bare separator
		// repeats the collapse when its turn comes, a content-carrying
		// "--- value" line is kept and opens the next document.
		j := i + 1
		for j < len(lines) && IsBlank(lines[j]) {
			j++
		}

		if j < len(lines) && isDocumentStartLine(lines[j]) {
			out = append(out, nil)

			continue
		}

		out = append(out, lines[i])
	}

	return bytes.Join(out, []byte("\n"))
}

// SplitDocumentBytes splits a normalized, empty-document-stripped YAML stream
// into per-document byte slices in source order, intended to align 1:1 with
// parser.ParseBytes's file.Docs. Documents are separated by bare "---" start
// markers or "..." end markers (see [isDocBoundaryLine]). Blank segments are
// dropped: a leading separator opens the stream with one, and an empty document
// between markers ("a: 1\n...\n\n...\nb: 2", which "..." leaves for
// [DropEmptyDocuments] to miss) leaves one in the middle. The parser emits no
// document for either, so keeping them would misalign the slices. Callers guard
// on the returned length matching the parsed document count and fall back to
// the whole input when it does not, so an imperfect split never changes behavior.
func SplitDocumentBytes(input []byte) [][]byte {
	lines := bytes.Split(input, []byte("\n"))

	segments := [][][]byte{nil}

	for _, line := range lines {
		if isDocBoundaryLine(line) {
			segments = append(segments, nil)

			continue
		}

		last := len(segments) - 1
		segments[last] = append(segments[last], line)
	}

	out := make([][]byte, 0, len(segments))

	for _, seg := range segments {
		joined := bytes.Join(seg, []byte("\n"))
		if IsBlank(joined) {
			continue
		}

		out = append(out, joined)
	}

	return out
}

// StripBOM removes a leading UTF-8 byte-order mark. A parser would otherwise
// treat it as part of the first property key.
func StripBOM(input []byte) []byte {
	return bytes.TrimPrefix(input, []byte("\xef\xbb\xbf"))
}

// NormalizeLineEndings folds CRLF and lone CR line breaks to LF. Returns the
// input unchanged when it contains no carriage returns.
func NormalizeLineEndings(input []byte) []byte {
	if !bytes.ContainsRune(input, '\r') {
		return input
	}

	input = bytes.ReplaceAll(input, []byte("\r\n"), []byte("\n"))

	return bytes.ReplaceAll(input, []byte("\r"), []byte("\n"))
}

// isSeparatorLine reports whether a line is a bare YAML document separator:
// "---" followed by nothing but whitespace or a whitespace-separated trailing
// comment ("--- # c"). A comment is not content, so a comment-carrying
// separator still opens an empty document. A separator carrying content
// ("--- value") opens a non-empty document and is not bare, and a marker
// fused to other characters ("---foo") is a plain scalar, not a separator.
func isSeparatorLine(line []byte) bool {
	rest, ok := bytes.CutPrefix(line, []byte("---"))
	if !ok {
		return false
	}

	rest = bytes.TrimRight(rest, " \t\r")
	if len(rest) == 0 {
		return true
	}

	// A trailing comment must be separated from the marker by whitespace;
	// without it the line is a plain scalar ("---#c"), not a separator.
	trimmed := bytes.TrimLeft(rest, " \t")

	return len(trimmed) < len(rest) && len(trimmed) > 0 && trimmed[0] == '#'
}

// isDocumentStartLine reports whether a line begins a document: a bare
// separator (see [isSeparatorLine]) or a "---" marker followed by whitespace
// and same-line content ("--- {b: 2}"). Either closes an empty document that
// a preceding bare separator opened, so [DropEmptyDocuments] collapses the
// bare separator when its look-ahead lands on one.
func isDocumentStartLine(line []byte) bool {
	if isSeparatorLine(line) {
		return true
	}

	rest, ok := bytes.CutPrefix(line, []byte("---"))
	if !ok {
		return false
	}

	return len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t')
}

// isDocBoundaryLine reports whether a line is a bare YAML document delimiter:
// the "---" start marker or the "..." end marker. Either separates two
// documents, so splitting on both keeps the per-document byte segments aligned
// with the parsed document list for "..."-delimited streams. It is kept
// distinct from [isSeparatorLine] because the two markers collapse differently
// when an empty document is dropped.
func isDocBoundaryLine(line []byte) bool {
	return isSeparatorLine(line) || bytes.Equal(bytes.TrimRight(line, " \t\r"), []byte("..."))
}

// IsBlank returns true if the byte slice contains only whitespace.
func IsBlank(data []byte) bool {
	for _, b := range data {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return false
		}
	}

	return true
}
