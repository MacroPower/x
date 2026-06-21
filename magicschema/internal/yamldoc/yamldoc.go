// Package yamldoc preprocesses raw YAML document bytes -- line-ending
// normalization, empty-document stripping, and per-document splitting --
// ahead of parsing.
package yamldoc

import "bytes"

// DropEmptyDocuments removes empty documents from a multi-document YAML
// stream by collapsing each "---" separator that is followed, across blank
// lines only, by another separator. The goccy/go-yaml parser stops emitting
// documents after an empty one ("a: 1\n---\n\n---\nb: 2" parses as two
// documents, losing b entirely), and empty documents contribute nothing to
// the union (a nil document body is skipped), so removing them up front
// preserves semantics while keeping later documents in the stream.
func DropEmptyDocuments(input []byte) []byte {
	// A bare separator line requires the "---" substring, so its absence means
	// there are no documents to collapse and the split/join would be a no-op.
	if !bytes.Contains(input, []byte("---")) {
		return input
	}

	lines := bytes.Split(input, []byte("\n"))

	out := make([][]byte, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		if !isSeparatorLine(lines[i]) {
			out = append(out, lines[i])

			continue
		}

		// Look ahead past blank lines; a following separator means this
		// separator opens an empty document, so drop it and the blanks.
		j := i + 1
		for j < len(lines) && IsBlank(lines[j]) {
			j++
		}

		if j < len(lines) && isSeparatorLine(lines[j]) {
			i = j - 1

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
// "---" with nothing but trailing whitespace. A separator carrying content
// ("--- value") opens a non-empty document and is not bare.
func isSeparatorLine(line []byte) bool {
	return bytes.Equal(bytes.TrimRight(line, " \t\r"), []byte("---"))
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
