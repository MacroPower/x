// Command godocfmt reformats Go doc comments.
//
// It wraps lines at a specified width (default 80), normalizes formatting, and
// preserves doc link brackets for symbols like [Foo] and [Type.Method].
//
// # Usage
//
//	godocfmt [flags] <file.go|directory> ...
//
// # Flags
//
//	-w WIDTH   wrap width (default 80)
//	-d         diff mode: show changes without writing
//	-l         list mode: only list files that would change
//
// # Formatting Rules
//
// The formatter applies the following rules to doc comments:
//
// Line wrapping: Text is wrapped at the specified width, accounting for comment
// prefixes and indentation.
//
// Sentence breaking: Each sentence ends its line.
// Sentences that span multiple lines are separated by blank lines for improved
// readability.
//
// Code blocks: Indented blocks (preceded by a blank line and starting with a
// tab) are preserved verbatim.
// Blank lines within code blocks remain empty without trailing whitespace.
//
// Lists: Bullet lists (using -, *, +) and numbered lists are normalized to the
// standard gofmt style with proper indentation.
//
// Headings: Lines starting with "# " are treated as headings and preserved with
// blank lines around them.
//
// Doc links: Brackets around documentation links like [Foo], [Type.Method], and
// [pkg.Name] are preserved for proper rendering by godoc.
//
// Link definitions: URL link definitions like [Name]: URL are collected and
// output at the end of the doc comment.
//
// Directive comments: Comments like //go:generate and //nolint are not
// reformatted and are preserved as-is.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/doc/comment"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

var (
	width    = flag.Int("w", 80, "wrap width")
	diffMode = flag.Bool("d", false, "diff mode: show changes without writing")
	listMode = flag.Bool("l", false, "list mode: only list files that would change")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: godocfmt [flags] <file.go|directory> ...\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	var files []string
	for _, arg := range flag.Args() {
		info, err := os.Stat(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", arg, err)
			os.Exit(1)
		}
		if info.IsDir() {
			err := filepath.Walk(arg, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !info.IsDir() && strings.HasSuffix(path, ".go") {
					files = append(files, path)
				}

				return nil
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", arg, err)
				os.Exit(1)
			}
		} else {
			files = append(files, arg)
		}
	}

	exitCode := 0
	for _, path := range files {
		changed, err := processFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)

			exitCode = 1

			continue
		}
		if changed && *listMode {
			fmt.Println(path)
		}
	}

	os.Exit(exitCode)
}

func processFile(path string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return false, err
	}

	result := src
	// Process comments in reverse order so byte offsets remain valid.
	groups := f.Comments
	for i := len(groups) - 1; i >= 0; i-- {
		cg := groups[i]
		if len(cg.List) == 0 {
			continue
		}

		// Only process // style comments.
		if !strings.HasPrefix(cg.List[0].Text, "//") {
			continue
		}

		// Skip directive comments (//nolint, //go:, etc.)
		if isDirectiveComment(cg) {
			continue
		}

		// Find the indentation before the first comment.
		start := fset.Position(cg.Pos()).Offset

		indent := ""
		for i := start - 1; i >= 0 && result[i] != '\n'; i-- {
			if result[i] == ' ' || result[i] == '\t' {
				indent = string(result[i]) + indent
			} else {
				indent = ""
			}
		}

		// Calculate effective width accounting for indent and "// " prefix.
		effectiveWidth := *width - len(indent) - 3

		// Extract comment text (strip // prefix).
		var lines []string
		for _, c := range cg.List {
			text := strings.TrimPrefix(c.Text, "//")
			if text != "" && text[0] == ' ' {
				text = text[1:]
			}

			lines = append(lines, text)
		}

		text := strings.Join(lines, "\n")

		// Parse and reformat using custom formatter that preserves doc link brackets.
		var p comment.Parser

		doc := p.Parse(text)
		formatted := formatDoc(doc, effectiveWidth)

		// Convert back to // comments with proper indentation.
		formatted = strings.TrimSuffix(formatted, "\n")

		var buf bytes.Buffer
		for i, line := range strings.Split(formatted, "\n") {
			if i > 0 {
				buf.WriteByte('\n')
				buf.WriteString(indent)
			}

			switch {
			case line == "":
				buf.WriteString("//")
			case line[0] == '\t':
				// Code block: no space after //.
				buf.WriteString("//")
				buf.WriteString(line)

			default:
				buf.WriteString("// ")
				buf.WriteString(line)
			}
		}

		// Replace in source.
		end := fset.Position(cg.End()).Offset
		result = append(result[:start], append(buf.Bytes(), result[end:]...)...)
	}

	if bytes.Equal(src, result) {
		return false, nil
	}

	if *diffMode {
		fmt.Printf("--- %s\n+++ %s\n", path, path)
		printDiff(string(src), string(result))

		return true, nil
	}

	if *listMode {
		return true, nil
	}

	return true, os.WriteFile(path, result, 0o644)
}

// isDirectiveComment returns true if the comment group contains directive
// comments that should not be reformatted (e.g., //nolint, //go:generate).
func isDirectiveComment(cg *ast.CommentGroup) bool {
	for _, c := range cg.List {
		text := strings.TrimPrefix(c.Text, "//")
		// Directive comments have no space after //.
		if text != "" && text[0] != ' ' && text[0] != '\t' {
			lower := strings.ToLower(text)
			if strings.HasPrefix(lower, "nolint") ||
				strings.HasPrefix(lower, "go:") ||
				strings.HasPrefix(lower, "lint:") ||
				strings.HasPrefix(text, "+build") {
				return true
			}
		}
	}

	return false
}

// formatDoc formats a parsed doc comment, preserving brackets around doc links.
func formatDoc(doc *comment.Doc, width int) string {
	var buf strings.Builder
	for i, block := range doc.Content {
		if i > 0 {
			buf.WriteString("\n")
		}

		formatBlock(&buf, block, width, "")
	}

	// Output link definitions.
	for _, link := range doc.Links {
		buf.WriteString("\n")
		buf.WriteString("[")
		buf.WriteString(link.Text)
		buf.WriteString("]: ")
		buf.WriteString(link.URL)
		buf.WriteString("\n")
	}

	return buf.String()
}

func formatBlock(buf *strings.Builder, block comment.Block, width int, indent string) {
	switch b := block.(type) {
	case *comment.Paragraph:
		formatParagraph(buf, b.Text, width, indent)
	case *comment.Heading:
		buf.WriteString("# ")

		for _, t := range b.Text {
			buf.WriteString(textString(t))
		}

		buf.WriteString("\n")

	case *comment.Code:
		code := strings.TrimSuffix(b.Text, "\n")
		for line := range strings.SplitSeq(code, "\n") {
			if line == "" {
				buf.WriteString("\n")
			} else {
				buf.WriteString("\t")
				buf.WriteString(line)
				buf.WriteString("\n")
			}
		}

	case *comment.List:
		for _, item := range b.Items {
			bullet := "  - "
			if item.Number != "" {
				bullet = fmt.Sprintf(" %s. ", item.Number)
			}

			buf.WriteString(bullet)
			// First paragraph inline with bullet.
			if len(item.Content) > 0 {
				if para, ok := item.Content[0].(*comment.Paragraph); ok {
					// Width minus bullet indent, continuation indent is 4 spaces.
					formatParagraphContinuation(buf, para.Text, width-len(bullet), "    ")
				}
			}
			// Remaining paragraphs indented.
			for k := 1; k < len(item.Content); k++ {
				buf.WriteString("\n")
				formatBlock(buf, item.Content[k], width-4, "    ")
			}
		}
	}
}

func formatParagraph(buf *strings.Builder, text []comment.Text, width int, indent string) {
	formatParagraphContinuation(buf, text, width, indent)
}

// formatParagraphContinuation formats paragraph text with sentence-aware
// breaking.
// Each sentence ends its line.
// Sentences spanning multiple lines get blank line separators.
func formatParagraphContinuation(buf *strings.Builder, text []comment.Text, width int, contIndent string) {
	// Collect all text into words.
	var words []string
	for _, t := range text {
		s := textString(t)
		words = append(words, strings.Fields(s)...)
	}

	if len(words) == 0 {
		buf.WriteString("\n")
		return
	}

	// Split words into sentences.
	sentences := splitIntoSentences(words)

	for i, sentence := range sentences {
		// Format this sentence with wrapping.
		sentenceWrapped := wrapSentence(sentence, width, contIndent)

		// Count lines in this sentence.
		lineCount := strings.Count(sentenceWrapped, "\n")

		// Add blank line before multi-line sentences (except the first one).
		if lineCount > 1 && i > 0 {
			buf.WriteString("\n")
		}

		buf.WriteString(sentenceWrapped)
	}
}

// splitIntoSentences splits words into sentence groups.
// A sentence ends when a word ends with ., ?, or ! And the next word starts
// with a capital letter or bracket.
func splitIntoSentences(words []string) [][]string {
	if len(words) == 0 {
		return nil
	}

	var (
		sentences [][]string
		current   []string
	)

	for i, word := range words {
		current = append(current, word)

		// Check if this word ends a sentence.
		if endsWithSentence(word) && i+1 < len(words) && startsNewSentence(words[i+1]) {
			sentences = append(sentences, current)
			current = nil
		}
	}

	// Don't forget the last sentence.
	if len(current) > 0 {
		sentences = append(sentences, current)
	}

	return sentences
}

// wrapSentence wraps a single sentence's words to fit within width.
func wrapSentence(words []string, width int, contIndent string) string {
	if len(words) == 0 {
		return "\n"
	}

	var buf strings.Builder

	lineLen := 0
	newLineWidth := width - len(contIndent)

	for i, word := range words {
		switch {
		case i == 0:
			buf.WriteString(word)

			lineLen = len(word)

		case lineLen+1+len(word) > width:
			buf.WriteString("\n")
			buf.WriteString(contIndent)
			buf.WriteString(word)

			lineLen = len(contIndent) + len(word)
			// After first wrap, use newLineWidth for subsequent lines.
			width = newLineWidth

		default:
			buf.WriteString(" ")
			buf.WriteString(word)

			lineLen += 1 + len(word)
		}
	}

	buf.WriteString("\n")

	return buf.String()
}

func endsWithSentence(word string) bool {
	if word == "" {
		return false
	}

	last := word[len(word)-1]

	return last == '.' || last == '?' || last == '!'
}

func startsNewSentence(word string) bool {
	if word == "" {
		return false
	}

	first := rune(word[0])
	// Starts with capital letter or bracket (doc link).
	return (first >= 'A' && first <= 'Z') || first == '['
}

func textString(t comment.Text) string {
	switch v := t.(type) {
	case comment.Plain:
		return string(v)
	case comment.Italic:
		return string(v)
	case *comment.Link:
		var text strings.Builder
		for _, inner := range v.Text {
			text.WriteString(textString(inner))
		}

		return "[" + text.String() + "]"

	case *comment.DocLink:
		return "[" + docLinkText(v) + "]"
	}

	return ""
}

func docLinkText(link *comment.DocLink) string {
	if link.ImportPath != "" {
		if link.Name != "" {
			return link.ImportPath + "." + link.Name
		}

		return link.ImportPath
	}
	if link.Recv != "" {
		return link.Recv + "." + link.Name
	}

	return link.Name
}

// printDiff prints a simple unified diff.
func printDiff(a, b string) {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")

	// Simple line-by-line diff (not optimal, but functional).
	ai, bi := 0, 0
	for ai < len(aLines) || bi < len(bLines) {
		switch {
		case ai >= len(aLines):
			fmt.Printf("+%s\n", bLines[bi])

			bi++

		case bi >= len(bLines):
			fmt.Printf("-%s\n", aLines[ai])

			ai++

		case aLines[ai] == bLines[bi]:
			fmt.Printf(" %s\n", aLines[ai])

			ai++
			bi++

		default:
			// Look ahead to find matching lines.
			found := false
			for lookahead := 1; lookahead < 5 && ai+lookahead < len(aLines); lookahead++ {
				if aLines[ai+lookahead] == bLines[bi] {
					for j := range lookahead {
						fmt.Printf("-%s\n", aLines[ai+j])
					}

					ai += lookahead
					found = true

					break
				}
			}
			if !found {
				for lookahead := 1; lookahead < 5 && bi+lookahead < len(bLines); lookahead++ {
					if bLines[bi+lookahead] == aLines[ai] {
						for j := range lookahead {
							fmt.Printf("+%s\n", bLines[bi+j])
						}

						bi += lookahead
						found = true

						break
					}
				}
			}
			if !found {
				fmt.Printf("-%s\n", aLines[ai])
				fmt.Printf("+%s\n", bLines[bi])

				ai++
				bi++
			}
		}
	}
}
