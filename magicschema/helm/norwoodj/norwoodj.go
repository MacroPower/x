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
	//nolint:goconst // JSON Schema type names repeated intentionally.
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
	// "# key.path -- description" format during ForContent.
	oldStyleDescs map[string]*parsedComment
}

// New creates a new helm-docs annotator.
func New() *Annotator {
	return &Annotator{}
}

// Name is the canonical annotator name, used as the registry key and in
// the --annotators flag.
const Name = "helm-docs"

// Name returns the annotator name.
func (a *Annotator) Name() string {
	return Name
}

// ForContent returns a new Annotator populated with old-style
// "# key.path -- description" entries parsed from the given file content.
func (a *Annotator) ForContent(content []byte) (magicschema.Annotator, error) {
	clone := &Annotator{
		oldStyleDescs: make(map[string]*parsedComment),
	}

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

		// A second old-style "# key.path -- desc" line begins a new block
		// rather than continuing the current one. Without this, two stacked
		// old-style comments merge: the first key absorbs the second comment's
		// text and the second key gets no annotation at all.
		if startsOldStyleBlock(line) {
			clone.finishOldStyleBlock(commentLines)

			commentLines = []string{line}

			continue
		}

		if commentContinuationRegex.MatchString(trimmed) {
			commentLines = append(commentLines, line)

			continue
		}

		// Non-comment line terminates the block.
		clone.finishOldStyleBlock(commentLines)

		commentLines = nil
		foundComment = false
	}

	// Handle trailing block at end of file.
	if foundComment && len(commentLines) > 0 {
		clone.finishOldStyleBlock(commentLines)
	}

	return clone, nil
}

// finishOldStyleBlock parses a collected old-style comment block
// (initial "# key -- desc" plus continuation lines) into an entry keyed by
// its key path.
func (a *Annotator) finishOldStyleBlock(commentLines []string) {
	if len(commentLines) == 0 {
		return
	}

	entry := parseCommentBlock(commentLines)
	if entry == nil || entry.keyPath == "" {
		return
	}

	a.oldStyleDescs[entry.keyPath] = entry
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

// cutNewStyleMarker reports whether a comment line is a new-style
// "# -- description" marker and, if so, returns the description text after it.
// The marker is recognized on the trimmed line, matching the "# --" detection
// used elsewhere (the issue #96 workaround and parseNewStyleComment), so old-
// style "# key.path -- desc" lines (which do not start with "# --") fall
// through to the regex.
func cutNewStyleMarker(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "# --")
	if !ok {
		return "", false
	}

	return strings.TrimSpace(rest), true
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
		// New-style "# -- description" is detected before the regex,
		// which requires whitespace before "--": the new-style marker sits
		// directly after the comment hash, so the regex would instead bind
		// the LAST " -- " on the line, misreading a description that itself
		// contains " -- " (e.g. "# -- see -- here") as an old-style key path
		// and dropping the real description.
		if rest, ok := cutNewStyleMarker(line); ok {
			keyPath = ""
			description = rest
			docStartIdx = i

			break
		}

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

	// Only strip the leading parenthetical when it actually names a type:
	// an empty "()" carries no type, and upstream helm-docs keeps it in the
	// description (it gates on valueTypeMatch[1] != "").
	if tm := helmDocsTypeRegex.FindStringSubmatch(description); len(tm) > 2 && tm[1] != "" {
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

		if markerToken(stripped, "@ignore") {
			skip = true

			continue
		}

		// Consume @notationType and @section without " -- " separator as a
		// divergence from upstream. Upstream would let these fall through to
		// continuation text. We consume them to avoid annotation markers
		// leaking into schema descriptions.
		if markerToken(stripped, "@notationType") || markerToken(stripped, "@section") {
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

	// Reconcile with the old-style "# key.path -- desc" entry parsed in
	// ForContent. A head comment that contributes only a @default (a
	// standalone "# @default --" with no "# --" description line) must not
	// shadow the old-style description and type; combine them instead, with
	// the node-level @default overriding any old-style default.
	if old, ok := a.oldStyleDescs[keyPath]; ok {
		switch {
		case entry == nil:
			entry = old
		case entry.description == "" && entry.typeName == "" && !entry.skip && entry.defaultVal != nil:
			if strings.TrimSpace(*entry.defaultVal) == "" {
				// A standalone empty "# @default --" carries no value; it must
				// not replace a meaningful old-style default with null. Keep
				// the old-style entry, default and all.
				entry = old
			} else {
				merged := *old
				merged.defaultVal = entry.defaultVal
				entry = &merged
			}

		case entry.defaultVal == nil:
			// A new-style entry that sets a description or type but no default
			// still inherits the old-style @default (per-field precedence),
			// rather than discarding it along with the rest of the old entry.
			entry.defaultVal = old.defaultVal
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
		// @default values are YAML expressions, so numbers, booleans, and
		// objects keep their native types (matching the bitnami annotator).
		schema.Default = magicschema.ParseYAMLValue(*entry.defaultVal)
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

	// A trailing comment after the last key attaches as the foot comment;
	// include it so @ignore placed there is honored, matching how the
	// losisin annotator scans foot comments.
	if mvn.FootComment != nil {
		parts = append(parts, mvn.FootComment.String())
	}

	return strings.Join(parts, "\n")
}

// parseNewStyleComment parses the new-style "# -- description" format from
// a comment block. Delegates to parseCommentBlock which handles the "last
// # -- group" workaround, continuation, @raw, @default, etc.
// Also extracts standalone @default when no "# --" line is present.
func (a *Annotator) parseNewStyleComment(commentStr string) *parsedComment {
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
				return &parsedComment{defaultVal: &val}
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

	return pc
}

// markerToken reports whether s begins with marker as a whole token -- the
// marker exactly, or followed by whitespace. A continuation line such as
// "@sections of the chart are documented below" then stays in the description
// instead of being silently consumed as an "@section" annotation.
func markerToken(s, marker string) bool {
	rest, ok := strings.CutPrefix(s, marker)
	if !ok {
		return false
	}

	return rest == "" || rest[0] == ' ' || rest[0] == '\t'
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

// startsOldStyleBlock reports whether a comment line begins a new old-style
// "# key.path -- description" block: it matches the description regex and the
// text before " -- " is a single key-path-like token (no spaces) that is not a
// recognized annotation marker. A continuation line, or prose that merely
// contains " -- ", does not qualify, so a single block's continuation stays
// intact while two stacked old-style comments split into separate blocks.
func startsOldStyleBlock(line string) bool {
	m := helmDocsDescRegex.FindStringSubmatch(strings.TrimSpace(line))
	if len(m) == 0 {
		return false
	}

	key := strings.TrimSpace(m[1])

	return key != "" && !strings.ContainsAny(key, " \t") && !isIgnoredHelmDocsAnnotation(key)
}

// isIgnoredHelmDocsAnnotation returns true if the content (after stripping
// the comment prefix) is a recognized annotation marker that should not
// leak into descriptions or be parsed as old-style key descriptions.
// Recognition delegates to the central [magicschema.IsAnnotationComment]
// list rather than maintaining a second marker set here.
func isIgnoredHelmDocsAnnotation(content string) bool {
	return magicschema.IsAnnotationComment(content)
}
