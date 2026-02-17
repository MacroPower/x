package norwoodj

import (
	"regexp"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/magicschema"
)

var (
	// HelmDocsDescRegex matches helm-docs description lines in both old-style
	// ("# key.path -- desc") and new-style ("# -- desc") formats.
	// Mirrors upstream's ^\s*#\s*(.*)\s+--\s*(.*)$.
	helmDocsDescRegex = regexp.MustCompile(`^\s*#\s*(.*)\s+--\s*(.*)$`)

	// HelmDocsTypeRegex extracts optional type hints from description text.
	// Matches "(type) remaining description" at the start of a description.
	helmDocsTypeRegex = regexp.MustCompile(`^\((.*?)\)\s*(.*)$`)

	// CommentContinuationRegex matches YAML comment lines for continuation.
	// Mirrors upstream's ^\s*#(\s?)(.*)$.
	commentContinuationRegex = regexp.MustCompile(`^\s*#(\s?)(.*)$`)

	// DefaultValueRegex matches @default override lines.
	defaultValueRegex = regexp.MustCompile(`^\s*# @default -- (.*)$`)

	// RawDescriptionRegex matches @raw annotation lines.
	rawDescriptionRegex = regexp.MustCompile(`^\s*#\s+@raw`)

	// NotationTypeRegex matches @notationType annotations with " -- " separator.
	// Mirrors upstream's ^\s*#\s+@notationType\s+--\s+(.*)$.
	notationTypeRegex = regexp.MustCompile(`^\s*#\s+@notationType\s+--\s+(.*)$`)

	// SectionRegex matches @section annotations with " -- " separator.
	// Mirrors upstream's ^\s*# @section -- (.*)$.
	sectionRegex = regexp.MustCompile(`^\s*# @section -- (.*)$`)

	// TypeMapping maps helm-docs type hints to JSON Schema types.
	typeMapping = map[string]string{
		"int":     "integer",
		"float":   "number",
		"bool":    "boolean",
		"list":    "array",
		"object":  "object",
		"dict":    "object",
		"string":  "string",
		"tpl":     "string",
		"yaml":    "string",
		"integer": "integer",
		"number":  "number",
		"boolean": "boolean",
		"array":   "array",
	}
)

// Annotator parses # -- description annotations from helm-docs.
type Annotator struct {
	// OldStyleDescs maps key paths to descriptions found via old-style
	// "# key.path -- description" format during Prepare.
	oldStyleDescs map[string]*helmDocsEntry
}

type helmDocsEntry struct {
	defaultVal  *string
	description string
	typeName    string
	skip        bool
}

// New creates a new helm-docs annotator.
func New() *Annotator {
	return &Annotator{}
}

// Name returns the annotator name.
func (a *Annotator) Name() string {
	return "helm-docs"
}

// Prepare scans file content for old-style "# key.path -- description" lines,
// including their continuation lines and inline annotations (@default, @raw,
// @section, @notationType), matching the upstream helm-docs file-level scanner.
func (a *Annotator) Prepare(content []byte) error {
	a.oldStyleDescs = make(map[string]*helmDocsEntry)

	allLines := strings.Split(string(content), "\n")
	foundComment := false

	var commentLines []string

	for _, line := range allLines {
		if !foundComment {
			// Look for an old-style "# key -- description" line.
			m := helmDocsDescRegex.FindStringSubmatch(strings.TrimSpace(line))
			if len(m) == 0 || strings.TrimSpace(m[1]) == "" {
				continue
			}

			// Verify key is not empty and not a recognized annotation.
			keyPart := strings.TrimSpace(m[1])
			if keyPart == "" || isIgnoredHelmDocsAnnotation(keyPart) {
				continue
			}

			foundComment = true

			commentLines = append(commentLines, line)

			continue
		}

		// Already found a key line; collect continuation lines.
		// Continuation lines are comment lines matching the upstream
		// commentContinuationRegex (^\\s*#(\\s?)(.*)$), @default,
		// @section, @raw, or @notationType patterns.
		trimmed := strings.TrimSpace(line)
		if commentContinuationRegex.MatchString(trimmed) {
			commentLines = append(commentLines, line)

			continue
		}

		// Non-comment line terminates the block.
		a.finishOldStyleBlock(commentLines)

		commentLines = nil
		foundComment = false
	}

	// Handle trailing block at end of file.
	if foundComment && len(commentLines) > 0 {
		a.finishOldStyleBlock(commentLines)
	}

	return nil
}

// finishOldStyleBlock parses a collected old-style comment block
// (initial "# key -- desc" plus continuation lines) into a helmDocsEntry.
func (a *Annotator) finishOldStyleBlock(commentLines []string) {
	if len(commentLines) == 0 {
		return
	}

	entry := parseCommentBlock(commentLines)
	if entry == nil || entry.keyPath == "" {
		return
	}

	a.oldStyleDescs[entry.keyPath] = &helmDocsEntry{
		description: entry.description,
		typeName:    entry.typeName,
		defaultVal:  entry.defaultVal,
		skip:        entry.skip,
	}
}

// parsedComment holds the result of parsing a comment block (old-style or
// new-style) through the shared parseCommentBlock logic.
type parsedComment struct {
	defaultVal  *string
	keyPath     string
	description string
	typeName    string
	skip        bool
}

// parseCommentBlock parses a block of comment lines using the same algorithm
// as upstream helm-docs ParseComment. It handles the "last # -- group"
// workaround, continuation lines, @raw, @default, @notationType, and @section.
// Lines matching @notationType and @section are recognized but do not produce
// schema output (they are consumed to avoid leaking into descriptions).
func parseCommentBlock(commentLines []string) *parsedComment {
	if len(commentLines) == 0 {
		return nil
	}

	// Scan ALL lines for @default before applying the issue #96
	// workaround, so that @default lines appearing before the last
	// "# --" group are preserved.
	var prefixDefault *string
	for _, line := range commentLines {
		if dm := defaultValueRegex.FindStringSubmatch(line); len(dm) > 1 {
			val := dm[1]
			prefixDefault = &val
		}
	}

	// Work around issue #96: if multiple "# --" lines exist, take only the
	// last group (from the last "# --" line onward) and recurse.
	lastIdx := 0
	for i, line := range commentLines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# --") {
			lastIdx = i
		}
	}

	if lastIdx > 0 {
		pc := parseCommentBlock(commentLines[lastIdx:])
		if pc != nil && pc.defaultVal == nil {
			pc.defaultVal = prefixDefault
		}

		return pc
	}

	// Find the description line matching "# key -- description" or "# -- description".
	// The regex includes the "# " prefix so it only matches comment lines.
	var (
		keyPath     string
		description string
		docStartIdx int
	)

	for i, line := range commentLines {
		m := helmDocsDescRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		keyPath = strings.TrimSpace(m[1])
		description = strings.TrimSpace(m[2])
		docStartIdx = i

		break
	}

	// Extract type hint from description.
	var typeName string
	if tm := helmDocsTypeRegex.FindStringSubmatch(description); tm != nil {
		typeName = mapHelmDocsType(strings.TrimSpace(tm[1]))
		description = strings.TrimSpace(tm[2])
	}

	// Process remaining lines for continuation, @raw, @default, @ignore.
	isRaw := false
	skip := false

	var defaultVal *string

	for _, line := range commentLines[docStartIdx+1:] {
		// Check @raw.
		if !isRaw && rawDescriptionRegex.MatchString(line) {
			isRaw = true

			continue
		}

		// Check @default.
		if dm := defaultValueRegex.FindStringSubmatch(line); len(dm) > 1 {
			val := dm[1]
			defaultVal = &val

			continue
		}

		// Check @notationType (with " -- " separator, matching upstream regex).
		if notationTypeRegex.MatchString(line) {
			continue
		}

		// Check @section (with " -- " separator, matching upstream regex).
		if sectionRegex.MatchString(line) {
			continue
		}

		// Check @ignore (recognized, consumed).
		stripped := strings.TrimSpace(line)
		stripped = strings.TrimPrefix(stripped, "#")
		stripped = strings.TrimSpace(stripped)

		if strings.HasPrefix(stripped, "@ignore") {
			skip = true

			continue
		}

		// Consume @notationType and @section without " -- " separator as a
		// divergence from upstream. Upstream would let these fall through to
		// continuation text. We consume them to avoid annotation markers
		// leaking into schema descriptions.
		if strings.HasPrefix(stripped, "@notationType") || strings.HasPrefix(stripped, "@section") {
			continue
		}

		// Comment continuation.
		cm := commentContinuationRegex.FindStringSubmatch(line)
		if cm == nil {
			continue
		}

		content := cm[2]

		if isRaw {
			description += "\n" + content
		} else {
			description += " " + content
		}
	}

	// Use prefix default if no default was found in continuation lines.
	if defaultVal == nil {
		defaultVal = prefixDefault
	}

	return &parsedComment{
		keyPath:     keyPath,
		description: description,
		typeName:    typeName,
		defaultVal:  defaultVal,
		skip:        skip,
	}
}

// Annotate extracts schema annotations from # -- comments.
func (a *Annotator) Annotate(node ast.Node, keyPath string) *magicschema.AnnotationResult {
	mvn, ok := node.(*ast.MappingValueNode)
	if !ok {
		return nil
	}

	// Collect all comment text from the node for @ignore checking.
	// Helm-docs uses strings.Contains(comment, "@ignore") as a
	// substring check on HeadComment.
	commentStr := collectComments(mvn)

	if strings.Contains(commentStr, "@ignore") {
		return &magicschema.AnnotationResult{Skip: true}
	}

	// Try new-style "# -- description" from the head comment.
	// Only use the head comment for new-style parsing (not inline comments).
	var headComment string
	if c := mvn.GetComment(); c != nil {
		headComment = c.String()
	}

	entry := a.parseNewStyleComment(headComment)

	// Fall back to old-style key-prefixed comment from Prepare.
	if entry == nil {
		if e, ok := a.oldStyleDescs[keyPath]; ok {
			entry = e
		}
	}

	if entry == nil {
		return nil
	}

	if entry.skip {
		return &magicschema.AnnotationResult{Skip: true}
	}

	schema := &jsonschema.Schema{
		Description: entry.description,
	}

	if entry.typeName != "" {
		schema.Type = entry.typeName
	}

	if entry.defaultVal != nil {
		schema.Default = magicschema.DefaultValue(*entry.defaultVal)
	}

	return &magicschema.AnnotationResult{Schema: schema}
}

// collectComments gathers all comment text from a MappingValueNode,
// including head comments and inline comments on key/value nodes.
func collectComments(mvn *ast.MappingValueNode) string {
	var parts []string

	if c := mvn.GetComment(); c != nil {
		parts = append(parts, c.String())
	}

	if mvn.Value != nil {
		if c := mvn.Value.GetComment(); c != nil {
			parts = append(parts, c.String())
		}
	}

	if keyNode, ok := mvn.Key.(ast.Node); ok {
		if c := keyNode.GetComment(); c != nil {
			parts = append(parts, c.String())
		}
	}

	return strings.Join(parts, "\n")
}

// parseNewStyleComment parses the new-style "# -- description" format from
// a comment block. Delegates to parseCommentBlock which handles the "last
// # -- group" workaround, continuation, @raw, @default, etc.
// Also extracts standalone @default when no "# --" line is present.
func (a *Annotator) parseNewStyleComment(commentStr string) *helmDocsEntry {
	lines := strings.Split(commentStr, "\n")

	// Check if there's any "# --" line at all.
	hasDesc := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# --") {
			hasDesc = true

			break
		}
	}

	if !hasDesc {
		// No "# --" line, but check for standalone @default.
		for _, line := range lines {
			if dm := defaultValueRegex.FindStringSubmatch(line); len(dm) > 1 {
				val := dm[1]
				return &helmDocsEntry{defaultVal: &val}
			}
		}

		return nil
	}

	pc := parseCommentBlock(lines)
	if pc == nil {
		return nil
	}

	// If parseCommentBlock returned a non-empty keyPath, this is an
	// old-style "# key.path -- desc" comment embedded in the head comment.
	// The upstream getDescriptionFromNode discards the auto-description in
	// this case and lets the old-style file scanner handle it. We do the
	// same: return nil so that Annotate falls through to oldStyleDescs.
	if pc.keyPath != "" {
		return nil
	}

	return &helmDocsEntry{
		description: pc.description,
		typeName:    pc.typeName,
		defaultVal:  pc.defaultVal,
		skip:        pc.skip,
	}
}

// mapHelmDocsType maps a helm-docs type hint to a JSON Schema type.
func mapHelmDocsType(hint string) string {
	// Direct mapping.
	if t, ok := typeMapping[hint]; ok {
		return t
	}

	// Compound types: use the last segment.
	if strings.Contains(hint, "/") {
		parts := strings.Split(hint, "/")
		last := parts[len(parts)-1]

		if t, ok := typeMapping[last]; ok {
			return t
		}
	}

	// Unrecognized type: silently ignored.
	return ""
}

// isIgnoredHelmDocsAnnotation returns true if the content (after stripping
// the comment prefix) is a recognized helm-docs annotation that should not
// leak into descriptions or be parsed as old-style key descriptions.
func isIgnoredHelmDocsAnnotation(content string) bool {
	trimmed := strings.TrimSpace(content)

	return strings.HasPrefix(trimmed, "@notationType") ||
		strings.HasPrefix(trimmed, "@section") ||
		strings.HasPrefix(trimmed, "@default") ||
		strings.HasPrefix(trimmed, "@raw") ||
		strings.HasPrefix(trimmed, "@ignore")
}
