// Package yamldoc preprocesses raw YAML document bytes -- line-ending
// normalization, empty-document stripping, and block-scalar masking for
// line-oriented annotation scans -- ahead of parsing.
package yamldoc

import (
	"bytes"
	"regexp"
	"strings"
)

// blockScalarHeader matches a line whose value position holds only a block
// scalar indicator with optional chomping/indentation modifiers, optionally
// preceded by anchor and tag tokens: "key: |", "- >-", "key: |2+",
// "key: &tpl !!str |". Anchors (&name) and tags (!tag) are the only tokens
// YAML permits between the key separator and the indicator, and a plain
// scalar cannot begin with '&' or '!', so allowing them cannot reintroduce
// the "cmd: foo | bar" false positive. The indicator must end the line;
// [isBlockScalarHeader] cuts any trailing comment before matching.
var (
	blockScalarHeader = regexp.MustCompile(`[:\-][ \t]+(?:[&!][^ \t]+[ \t]+)*[|>]\d*[+-]?\d*[ \t]*$`)

	// The entryScalarRest pattern matches a sequence entry's remainder when
	// the entry itself holds the block scalar ("- |", "- &tpl >-"): optional
	// anchor/tag tokens, then the indicator. A mapping key there instead
	// ("- script: |") makes the key the scalar's owner (see [headerIndentOf]).
	entryScalarRest = regexp.MustCompile(`^(?:[&!][^ \t]+[ \t]+)*[|>]`)
)

// isBlockScalarHeader reports whether a line opens a block scalar. The
// trailing comment is cut before matching -- a '#' preceded by whitespace
// starts a comment outside quoted scalars per YAML -- so an indicator-like
// suffix inside a comment ("image: # config: |") never opens a scalar, while
// a real header's own trailing comment ("key: | # c") still matches.
func isBlockScalarHeader(line string) bool {
	cut := len(line)

	for i := 1; i < len(line); i++ {
		if line[i] == '#' && (line[i-1] == ' ' || line[i-1] == '\t') {
			cut = i

			break
		}
	}

	return blockScalarHeader.MatchString(line[:cut])
}

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
		if !strings.HasPrefix(trimmed, "#") && isBlockScalarHeader(line) {
			inScalar, headerIndent = true, headerIndentOf(line)
		}
	}

	return lines
}

// indentOf counts a line's leading spaces. YAML forbids tabs in indentation,
// so spaces alone determine nesting.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// headerIndentOf returns the column of a block scalar header's owner: the
// first character past the leading spaces and any "- " sequence-entry
// markers when the scalar hangs off a mapping key ("- script: |" owns the
// scalar at the key's column), or the innermost dash's own column when the
// indicator directly follows the dashes ("- |"). Block scalar content must
// be indented past the owner, so a sequence-entry sibling key ("  other:"
// beside "- script: |") sits at the owner's indent and ends the scalar
// instead of being masked as interior. A raw leading-space count would put
// the owner at the dash and swallow every sibling.
func headerIndentOf(line string) int {
	i, lastDash := 0, -1

loop:
	for i < len(line) {
		switch {
		case line[i] == ' ':
			i++
		case line[i] == '-' && i+1 < len(line) && (line[i+1] == ' ' || line[i+1] == '\t'):
			lastDash = i

			i += 2

		default:
			break loop
		}
	}

	if lastDash >= 0 && entryScalarRest.MatchString(line[i:]) {
		return lastDash
	}

	return i
}

// DropEmptyDocuments removes empty documents from a multi-document YAML
// stream by blanking each bare "---" separator that is followed, across
// blank lines only, by another document boundary -- another bare separator,
// a "---" marker carrying same-line content ("--- {b: 2}"), or a bare "..."
// end marker (which goccy otherwise fails to parse directly after "---").
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

		// Look ahead past blank lines; a following document start line or a
		// bare "..." end marker means this separator opens an empty document,
		// so blank it out. The blank run and the boundary line keep their own
		// lines: a bare separator repeats the collapse when its turn comes, a
		// content-carrying "--- value" line is kept and opens the next
		// document, and a kept "..." harmlessly terminates the previous
		// document -- goccy otherwise fails the whole parse on the valid
		// stream "---\n...".
		j := i + 1
		for j < len(lines) && IsBlank(lines[j]) {
			j++
		}

		if j < len(lines) && (isDocumentStartLine(lines[j]) || isEndMarkerLine(lines[j])) {
			out = append(out, nil)

			continue
		}

		out = append(out, lines[i])
	}

	return bytes.Join(out, []byte("\n"))
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

// isEndMarkerLine reports whether a line is a bare "..." document end marker.
func isEndMarkerLine(line []byte) bool {
	return bytes.Equal(bytes.TrimRight(line, " \t\r"), []byte("..."))
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
