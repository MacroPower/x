package norwoodj

import (
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/internal/yamldoc"
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

	// RawDescriptionRegex matches @raw annotation lines. @raw is anchored as a
	// whole token (followed by whitespace or end of line) so "@rawData" is not
	// mistaken for it, and a space after the hash is required, matching upstream.
	rawDescriptionRegex = regexp.MustCompile(`^\s*#\s+@raw(\s|$)`)

	// TypeMapping maps helm-docs type hints to JSON Schema types. The tpl and
	// yaml render notations map to "string" only during compound-hint
	// resolution (list/tpl -> array, tpl/string -> string); mapHelmDocsType
	// asserts no type for the bare hints, which sit on mappings and sequences
	// as readily as on strings.
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

	foundComment := false

	var commentLines []string

	// Block scalar interiors are blanked before the scan: string data inside
	// a "|" or ">" value may look exactly like an old-style "# key -- desc"
	// line, and registering it would attach a wrong description to a real
	// key. A blanked line reads as a non-comment line, terminating any open
	// block the same way the data line itself would.
	for _, line := range yamldoc.MaskBlockScalars(content) {
		if !foundComment {
			// Look for an old-style "# key.path -- description" line. The
			// same predicate splits stacked blocks below, so the scan that
			// opens a block and the split that starts the next one always
			// agree; its key-path check is also shared with the fallback
			// comment extractor (see [magicschema.IsHelmDocsKeyPath]).
			if !startsOldStyleBlock(line) {
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

	// Handle the trailing block at end of file. The finishOldStyleBlock helper
	// no-ops on an empty slice, and commentLines is non-empty whenever
	// foundComment is set.
	if foundComment {
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

	// Bracketed array indices normalize to the walker's index-free paths
	// ("jobs[0].name" resolves as "jobs.name", applied to every element via
	// the items schema), the same rule bitnami applies; without it the entry
	// is stored under a path the walker never asks for, and the comment is
	// simultaneously suppressed from the fallback description, so the
	// annotation would apply nowhere. An element-level path ("items[0]")
	// drops entirely rather than mis-attaching to the array key.
	keyPath, ok := magicschema.NormalizeKeyPath(entry.keyPath)
	if !ok {
		return
	}

	a.oldStyleDescs[keyPath] = entry
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

// lastDefault returns the value of the last "# @default -- value" line in the
// block, or nil when there is none. @default resolves last-wins, so a later
// override beats an earlier one regardless of where the description sits.
func lastDefault(lines []string) *string {
	var val *string

	for _, line := range lines {
		if dm := defaultValueRegex.FindStringSubmatch(line); len(dm) > 1 {
			v := dm[1]
			val = &v
		}
	}

	return val
}

// splitOldStyleComment matches an old-style "# key.path -- description" line
// and returns the key path and description, splitting on the FIRST " -- "
// separator. The greedy first capture in helmDocsDescRegex otherwise swallows
// every "-- " up to the last one, which both mis-keys the entry and trips the
// IsAnnotationComment guard when the description itself contains " -- " (the
// real key is then dropped). Rejoining the extra separators onto the
// description preserves it, mirroring the new-style cutNewStyleMarker handling.
// The boolean is false when the line is not a description line at all.
func splitOldStyleComment(line string) (string, string, bool) {
	m := helmDocsDescRegex.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}

	key := strings.TrimSpace(m[1])
	desc := strings.TrimSpace(m[2])

	if idx := strings.Index(key, " -- "); idx >= 0 {
		desc = strings.TrimSpace(key[idx+len(" -- "):]) + " -- " + desc
		key = strings.TrimSpace(key[:idx])
	}

	return key, desc, true
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
	prefixDefault := lastDefault(commentLines)

	// Work around issue #96: if multiple "# --" lines exist, take only the
	// last group (from the last "# --" line onward) and recurse.
	lastIdx := 0
	for i, line := range commentLines {
		if _, ok := cutNewStyleMarker(line); ok {
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

		k, d, ok := splitOldStyleComment(line)
		if !ok {
			continue
		}

		keyPath = k
		description = d
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

	for _, line := range commentLines[docStartIdx+1:] {
		// @raw switches continuation joining to newline mode. It keeps its own
		// regex because, unlike @ignore, it requires a space after the hash.
		if !isRaw && rawDescriptionRegex.MatchString(line) {
			isRaw = true

			continue
		}

		// @default lines are only consumed here: resolution is one last-wins
		// rule over the whole block, and prefixDefault already holds the
		// winning value -- any match after docStartIdx is by index order also
		// the last match lastDefault found.
		if defaultValueRegex.MatchString(line) {
			continue
		}

		// The remaining markers are recognized on the shared two-hash-capped
		// strip, so they are detected consistently across annotators (a two-hash
		// "## @ignore" is honored) and markerToken anchors on a whole token, so
		// "@sections of the chart" stays description text.
		stripped := strings.TrimSpace(magicschema.StripCommentMarker(line))

		// @ignore hides the key.
		if markerToken(stripped, "@ignore") {
			skip = true

			continue
		}

		// @notationType and @section are recognized but produce no schema
		// output. Consuming both the " -- "-separated form and the bare form via
		// markerToken (a divergence from upstream, which lets the bare form fall
		// through) keeps either from leaking into the description.
		if markerToken(stripped, "@notationType") || markerToken(stripped, "@section") {
			continue
		}

		// Comment continuation.
		cm := commentContinuationRegex.FindStringSubmatch(line)
		if cm == nil {
			continue
		}

		content := cm[2]

		// Join onto the accumulated description. Outside raw mode an empty
		// description is seeded directly (a bare "# --" marker) so the result
		// does not begin with a stray separator. In raw mode the empty state
		// still needs the leading newline, so the raw branch handles it even
		// from empty -- dropping it would swallow the leading blank lines that
		// raw mode is meant to preserve.
		switch {
		case description == "" && !isRaw:
			description = content
		case isRaw:
			description += "\n" + content
		default:
			description += " " + content
		}
	}

	return &parsedComment{
		keyPath:     keyPath,
		description: description,
		typeName:    typeName,
		defaultVal:  prefixDefault,
		skip:        skip,
	}
}

// Annotate extracts schema annotations from # -- comments.
func (a *Annotator) Annotate(node ast.Node, keyPath string) *magicschema.AnnotationResult {
	mvn, ok := node.(*ast.MappingValueNode)
	if !ok {
		return nil
	}

	// The shared collector narrows the head comment to the run that
	// physically documents the key ([magicschema.HeadCommentRun], matching
	// upstream's yaml.v3 HeadComment, which excludes blank-line-separated
	// blocks) and excludes a sequence's stowed first-element head comment
	// from the value-line part -- for such a comment upstream removes only
	// the matching sequence item, never the whole key.
	nc := magicschema.CollectNodeComments(mvn)
	headComment := nc.HeadRun

	// Collect all comment text from the node for @ignore checking, foot
	// comment included so an @ignore placed there is honored. Helm-docs uses
	// strings.Contains(comment, "@ignore") as a substring check.
	var parts []string

	for _, part := range []string{nc.HeadRun, nc.ValueInline, nc.KeyInline, nc.Foot} {
		if part != "" {
			parts = append(parts, part)
		}
	}

	commentStr := strings.Join(parts, "\n")

	if strings.Contains(commentStr, "@ignore") {
		return &magicschema.AnnotationResult{Skip: true}
	}

	// Try new-style "# -- description" from the head comment.
	// Only use the head comment for new-style parsing (not inline comments).
	entry := a.parseNewStyleComment(headComment)

	// Reconcile with the old-style "# key.path -- desc" entry parsed in
	// ForContent.
	if old, ok := a.oldStyleDescs[keyPath]; ok {
		entry = reconcileOldStyle(entry, old)
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

// reconcileOldStyle combines a new-style entry parsed from a node's head
// comment with the old-style entry the file scan recorded for the same key.
// A head comment contributing only a @default (a standalone "# @default --"
// with no "# --" description line) must not shadow the old-style description
// and type, so it combines with the old entry, the node-level @default winning.
// Otherwise the new-style entry wins per field, with the old-style entry
// filling only the fields it leaves unset (a description-only override keeps the
// old type hint and @default; a type-only override keeps the old description).
func reconcileOldStyle(entry, old *parsedComment) *parsedComment {
	switch {
	case entry == nil:
		return old

	case entry.description == "" && entry.typeName == "" && !entry.skip && entry.defaultVal != nil:
		if strings.TrimSpace(*entry.defaultVal) == "" {
			// A standalone empty "# @default --" carries no value; it must not
			// replace a meaningful old-style default with null.
			return old
		}

		merged := *old
		merged.defaultVal = entry.defaultVal

		return &merged

	default:
		if entry.description == "" {
			entry.description = old.description
		}

		if entry.typeName == "" {
			entry.typeName = old.typeName
		}

		if entry.defaultVal == nil {
			entry.defaultVal = old.defaultVal
		}

		// An old-style @ignore still hides the key even when the node carries
		// its own new-style comment; the node-level entry only overrides the
		// fields it sets, and skip is never cleared by a description override.
		if old.skip {
			entry.skip = true
		}

		return entry
	}
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
		if _, ok := cutNewStyleMarker(line); ok {
			hasDesc = true

			break
		}
	}

	if !hasDesc {
		// A block holding an old-style "# key.path -- desc" line documents
		// that key, not this node: its @default already reaches the right key
		// through the ForContent file scan (oldStyleDescs), so extracting it
		// here would attach the default to the physically following node --
		// the same misattribution the pc.keyPath guard below prevents for
		// descriptions.
		if slices.ContainsFunc(lines, startsOldStyleBlock) {
			return nil
		}

		// No "# --" line, but check for standalone @default. @default resolves
		// last-wins, matching parseCommentBlock's prefix scan so the result does
		// not depend on whether a description line is present.
		if d := lastDefault(lines); d != nil {
			return &parsedComment{defaultVal: d}
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
// marker exactly, or followed by whitespace ([magicschema.IsMarkerBoundary]).
// A continuation line such as "@sections of the chart are documented below"
// then stays in the description instead of being silently consumed as an
// "@section" annotation.
func markerToken(s, marker string) bool {
	rest, ok := strings.CutPrefix(s, marker)

	return ok && magicschema.IsMarkerBoundary(rest)
}

// mapHelmDocsType maps a helm-docs type hint to a JSON Schema type.
func mapHelmDocsType(hint string) string {
	// A bare tpl or yaml hint is a render notation -- the value is a Go
	// template, or is rendered as a YAML block -- not a type assertion, and
	// charts place it on mappings and sequences as readily as on strings.
	// Asserting "string" would reject the chart's own values, so the hint
	// contributes no type constraint and structural inference decides
	// (fail open).
	if isStringModifier(hint) {
		return ""
	}

	// Direct mapping.
	if t, ok := typeMapping[hint]; ok {
		return t
	}

	// A compound hint resolves only when its trailing /-separated segment names
	// a known element type. A custom notation like "list/csv" (a CSV-encoded
	// string) keeps falling through to structural inference.
	element, known := typeMapping[hint[strings.LastIndex(hint, "/")+1:]]
	if !strings.Contains(hint, "/") || !known {
		return ""
	}

	// When the FIRST segment is a container (list/dict/array/object) the
	// outermost container is the structural type, including nested hints like
	// "list/list/string" or "dict/foo/string" -- a scalar type must never be
	// asserted on a container value.
	first, _, _ := strings.Cut(hint, "/")
	if container, ok := typeMapping[first]; ok && isContainerType(container) {
		return container
	}

	// Otherwise the element type wins only when the leading segment is a scalar
	// modifier (tpl/string, tpl/array) -- an encoding wrapper, not a type. A
	// leading segment that is itself a concrete scalar type is contradictory
	// ("string/int"), and an unknown one cannot be read as a modifier, so both
	// fall through to structural inference rather than assert a type the value
	// may not match (fail open).
	if isStringModifier(first) {
		return element
	}

	return ""
}

// isStringModifier reports whether a type-hint segment is a render notation
// rather than a concrete type -- a string-encoding wrapper such as tpl (a Go
// template) or yaml (a YAML-encoded value). As the leading segment of a
// compound hint the element type wins (tpl/array -> array); as a bare hint it
// asserts no type at all, leaving the type to structural inference.
func isStringModifier(seg string) bool {
	return seg == "tpl" || seg == "yaml"
}

// isContainerType reports whether a JSON Schema type is a container (array or
// object) -- the kinds whose leading segment wins in a compound type hint.
func isContainerType(t string) bool {
	return t == "array" || t == "object" //nolint:goconst // JSON Schema type names
}

// startsOldStyleBlock reports whether a comment line begins a new old-style
// "# key.path -- description" block: it matches the description regex and the
// text before " -- " is a single key-path-like token (no spaces) that is not a
// recognized annotation marker. A continuation line, or prose that merely
// contains " -- ", does not qualify, so a single block's continuation stays
// intact while two stacked old-style comments split into separate blocks.
func startsOldStyleBlock(line string) bool {
	key, _, ok := splitOldStyleComment(strings.TrimSpace(line))

	return ok && magicschema.IsHelmDocsKeyPath(key) && !magicschema.IsAnnotationComment(key)
}
