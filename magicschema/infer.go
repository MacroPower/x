package magicschema

import (
	"slices"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"
)

// JSON Schema type constants.
const (
	typeBoolean = "boolean"
	typeInteger = "integer"
	typeNumber  = "number"
	typeString  = "string"
	typeArray   = "array"
	typeObject  = "object"
	typeNull    = "null"
)

// inferType returns the JSON Schema type string for the given YAML AST node.
// Returns an empty string for null/empty values (maximally permissive).
// An explicit YAML tag (such as !!str or !!int) is authoritative: YAML
// loaders coerce the scalar to the tagged type, so the schema reflects the
// tag even when the literal looks like another type. Unknown tags fall
// through to the underlying value node.
func inferType(node ast.Node) string {
unwrap:
	for {
		switch n := node.(type) {
		case *ast.AnchorNode:
			node = n.Value
		case *ast.TagNode:
			if t, ok := tagType(n.Start.Value); ok {
				return t
			}

			node = n.Value

		default:
			break unwrap
		}
	}

	switch node.(type) {
	case *ast.BoolNode:
		return typeBoolean
	case *ast.IntegerNode:
		return typeInteger
	case *ast.FloatNode:
		return typeNumber
	case *ast.InfinityNode, *ast.NanNode:
		return typeNumber
	case *ast.StringNode, *ast.LiteralNode:
		return typeString
	case *ast.SequenceNode:
		return typeArray
	case *ast.MappingNode, *ast.MappingValueNode:
		return typeObject
	case *ast.NullNode:
		return ""
	}

	return ""
}

// tagType maps an explicit YAML tag to its JSON Schema type. The boolean
// reports whether the tag determines a type; custom tags (!foo) and tags
// outside the core schema do not, and inference falls through to the
// underlying value node.
func tagType(tag string) (string, bool) {
	switch tag {
	case "!!str", "!!binary", "!!timestamp":
		return typeString, true
	case "!!int":
		return typeInteger, true
	case "!!float":
		return typeNumber, true
	case "!!bool":
		return typeBoolean, true
	case "!!null":
		return "", true
	case "!!seq":
		return typeArray, true
	case "!!map":
		return typeObject, true
	}

	return "", false
}

// unwrapNode resolves TagNode and AnchorNode wrappers to the underlying
// value node.
func unwrapNode(node ast.Node) ast.Node {
	for {
		switch n := node.(type) {
		case *ast.TagNode:
			node = n.Value
		case *ast.AnchorNode:
			node = n.Value
		default:
			return node
		}
	}
}

// extractComment extracts a plain-text description from a node's comments.
// Returns empty string if no suitable comment is found.
func extractComment(node ast.Node) string {
	mvn, ok := node.(*ast.MappingValueNode)
	if !ok {
		return ""
	}

	// Head comment block above the key, narrowed to the run that
	// physically documents it.
	if run := adjacentCommentRun(mvn.GetComment(), mvn.Key); len(run) > 0 {
		if desc := commentDescription(strings.Join(run, "\n")); desc != "" {
			return desc
		}
	}

	// Try inline comment on the value node.
	if mvn.Value != nil {
		if desc := extractFromComment(mvn.Value.GetComment()); desc != "" {
			return desc
		}
	}

	// Try inline comment on the key node.
	if keyNode, ok := mvn.Key.(ast.Node); ok {
		if desc := extractFromComment(keyNode.GetComment()); desc != "" {
			return desc
		}
	}

	return ""
}

// extractFromComment extracts a description from a comment group node.
func extractFromComment(comment *ast.CommentGroupNode) string {
	if comment == nil {
		return ""
	}

	return commentDescription(comment.String())
}

// commentDescription cleans a comment string and rejects results that are
// annotation markers rather than prose.
func commentDescription(s string) string {
	desc := cleanComment(s)
	if desc != "" && !IsAnnotationComment(desc) {
		return desc
	}

	return ""
}

// adjacentCommentRun returns the lines ("#"-prefixed, matching
// [ast.CommentGroupNode.String] output) of the head comment run that
// documents the key. The goccy parser merges separate comment blocks -- a
// file header, a commented-out example for the previous key -- into one
// head comment group on the following key, erasing the physical blank
// lines between them. Token positions reconstruct the layout: a run is a
// maximal sequence of comment tokens on consecutive lines at the same
// column, so a jump in line numbers is an erased blank line and a column
// change is a comment from a different nesting level (such as a
// commented-out child of the previous key). A description is the comment
// block touching the key, so only the final run counts, and only when its
// last line sits directly above the key and is not indented past the key's
// column; anything else is a stray block that documents nothing here.
// Comment tokens without position information cannot be placed, so the
// whole group is attributed (fail open).
func adjacentCommentRun(comment *ast.CommentGroupNode, key ast.MapKeyNode) []string {
	if comment == nil {
		return nil
	}

	var (
		all, run          []string
		prevLine, prevCol int
		unpositioned      bool
	)

	for _, c := range comment.Comments {
		tok := c.GetToken()
		if tok == nil {
			return nil
		}

		line := "#" + tok.Value
		all = append(all, line)

		if tok.Position == nil {
			unpositioned = true

			continue
		}

		if len(run) > 0 && (tok.Position.Line != prevLine+1 || tok.Position.Column != prevCol) {
			run = run[:0]
		}

		run = append(run, line)
		prevLine, prevCol = tok.Position.Line, tok.Position.Column
	}

	keyTok := key.GetToken()
	if unpositioned || keyTok == nil || keyTok.Position == nil {
		return all
	}

	if len(run) == 0 || prevLine != keyTok.Position.Line-1 || prevCol > keyTok.Position.Column {
		return nil
	}

	return run
}

// cleanComment strips comment markers and whitespace from a comment string.
// Multi-line comments are joined with newlines, and "#"-only lines separate
// paragraphs (rendered as blank lines, with runs collapsed and the ends
// trimmed): callers pass only the comment block that documents one key (see
// [adjacentCommentRun]), so a "#"-only line inside it is a paragraph
// separator within one description, not a boundary between unrelated
// comment groups. Indentation beyond the single space after "#" survives,
// so YAML snippets embedded in comments keep their structure. Annotation
// lines are dropped: bare @schema and @schema.root lines fence helm-schema
// blocks whose content is annotation data, not prose, and lines matching
// [IsAnnotationComment] are markers.
func cleanComment(s string) string {
	var (
		lines         []string
		inSchemaBlock bool
		inRootBlock   bool
	)

	for line := range strings.SplitSeq(s, "\n") {
		// Markers are matched on a fully trimmed copy; the content keeps
		// its indentation.
		content := stripCommentPrefix(line)
		cleaned := strings.TrimSpace(content)

		// Track helm-schema block fences so block content never leaks
		// into descriptions. A root marker inside an open @schema block
		// is junk content, not a delimiter, and a @schema delimiter also
		// ends an unclosed @schema.root block, mirroring the dadav
		// annotator's block extraction. Like upstream helm-schema, junk
		// suffixes such as "@schema@" still delimit a block; only a
		// whitespace-separated suffix is excluded, since that form is the
		// helm-values-schema inline annotation.
		if after, ok := strings.CutPrefix(cleaned, "@schema.root"); ok {
			if !inSchemaBlock && after == "" {
				inRootBlock = !inRootBlock
			}

			continue
		}

		if after, ok := strings.CutPrefix(cleaned, "@schema"); ok {
			if after == "" || (after[0] != ' ' && after[0] != '\t') {
				inRootBlock = false
				inSchemaBlock = !inSchemaBlock
			}

			// Inline @schema annotations (helm-values-schema format) are
			// annotation markers either way.
			continue
		}

		if inSchemaBlock || inRootBlock {
			continue
		}

		// Skip annotation markers from any supported annotator format so
		// they never leak into descriptions. Blank lines stay: they
		// separate paragraphs.
		if cleaned != "" && IsAnnotationComment(cleaned) {
			continue
		}

		lines = append(lines, content)
	}

	// Render paragraph separators as blank lines, collapsing runs and
	// trimming the ends so the description neither starts nor ends with
	// a separator.
	var (
		out       []string
		separator bool
	)

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			separator = len(out) > 0

			continue
		}

		if separator {
			out = append(out, "")

			separator = false
		}

		out = append(out, line)
	}

	return strings.Join(out, "\n")
}

// stripCommentPrefix removes leading "#" characters and a single space.
func stripCommentPrefix(line string) string {
	line = strings.TrimSpace(line)
	for strings.HasPrefix(line, "#") {
		line = strings.TrimPrefix(line, "#")
	}

	line = strings.TrimPrefix(line, " ")

	return line
}

// IsAnnotationComment returns true if the comment looks like an annotation
// marker from any of the supported annotators.
func IsAnnotationComment(s string) bool {
	s = strings.TrimSpace(s)

	return strings.HasPrefix(s, "@schema") ||
		strings.HasPrefix(s, "@param") ||
		strings.HasPrefix(s, "@skip") ||
		strings.HasPrefix(s, "@section") ||
		strings.HasPrefix(s, "@extra") ||
		strings.HasPrefix(s, "@descriptionStart") ||
		strings.HasPrefix(s, "@descriptionEnd") ||
		strings.HasPrefix(s, "@raw") ||
		strings.HasPrefix(s, "@ignore") ||
		strings.HasPrefix(s, "@notationType") ||
		strings.HasPrefix(s, "@default") ||
		strings.HasPrefix(s, "--") ||
		isHelmDocsOldStyleComment(s)
}

// isHelmDocsOldStyleComment detects old-style helm-docs comments of the form
// "key.path -- description" where a dotted key path precedes the " -- "
// separator. This prevents these comments from leaking as descriptions on
// parent nodes during fallback comment extraction.
func isHelmDocsOldStyleComment(s string) bool {
	idx := strings.Index(s, " -- ")
	if idx <= 0 {
		return false
	}

	// The part before " -- " should look like a dotted key path (e.g.,
	// "image.tag", "controller.service.annotations.\"key\""). A key path
	// is a single token: prose that happens to contain a dot, such as
	// "Use the v1.2 API -- stable", has spaces and is a legitimate
	// description.
	prefix := strings.TrimSpace(s[:idx])

	return strings.Contains(prefix, ".") && !strings.ContainsAny(prefix, " \t")
}

// inferItemsSchema creates an items schema from a sequence node's elements.
// Mixed element types widen, and a null or empty element among typed
// elements adds "null" to the items type so the source list validates.
// Returns nil (no constraint) for empty sequences, all-null sequences, and
// incompatible element types.
func inferItemsSchema(seq *ast.SequenceNode) *jsonschema.Schema {
	if len(seq.Values) == 0 {
		return nil
	}

	var (
		resultType string
		hasNull    bool
	)

	// Genuine nulls mark the list nullable; unknown nodes (aliases) stay
	// transparent via the empty string, which widens to the other side.
	for _, val := range seq.Values {
		if isNullNode(val) {
			hasNull = true

			continue
		}

		resultType = widenType(resultType, inferType(val))
	}

	// No positive type evidence, or incompatible types: fail open.
	if resultType == "" {
		return nil
	}

	if hasNull {
		return &jsonschema.Schema{Types: []string{resultType, typeNull}}
	}

	return &jsonschema.Schema{Type: resultType}
}

// isNullNode reports whether a node is a YAML null value: a null literal or
// a !!null-tagged scalar, looking through anchor wrappers. Aliases and other
// unresolved nodes are not nulls; they stay transparent to inference.
func isNullNode(node ast.Node) bool {
	for {
		switch n := node.(type) {
		case *ast.AnchorNode:
			node = n.Value
		case *ast.TagNode:
			// A known tag is authoritative: !!null is a null, any other
			// core tag is not. Unknown tags fall through to the value.
			if t, ok := tagType(n.Start.Value); ok {
				return t == ""
			}

			node = n.Value

		default:
			_, ok := node.(*ast.NullNode)

			return ok
		}
	}
}

// widenType returns the widened type when merging two type strings.
// Returns empty string (no constraint) for incompatible types. The empty
// string means unknown and merges transparently; callers separate genuine
// nulls before folding (see [inferItemsSchema]), and [widenTypeList] is the
// cross-input primitive that carries null instead.
func widenType(a, b string) string {
	if a == b {
		return a
	}

	// Unknown merges transparently.
	if a == "" {
		return b
	}

	if b == "" {
		return a
	}

	// Integer + number -> number.
	if (a == typeInteger && b == typeNumber) || (a == typeNumber && b == typeInteger) {
		return typeNumber
	}

	// All other combinations -> no constraint.
	return ""
}

// typeList returns a schema's type constraint as a list: the scalar Type
// field as a single-element list, or the Types union as-is.
func typeList(s *jsonschema.Schema) []string {
	if s.Type != "" {
		return []string{s.Type}
	}

	return s.Types
}

// widenTypeList merges two type lists, generalizing [widenType] to type
// unions. A side with no type constraint means the value was null or empty
// in that input, so the result is the other side's types plus "null" -- the
// null input must still validate against the merged schema. Two empty sides
// stay empty. Beyond that, identical sets merge, all-numeric sets widen to
// number, anything else drops the constraint entirely (fail open), and the
// "null" member carries through whenever either side allows null.
func widenTypeList(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}

	// Exactly one side has no type constraint: the value was null or
	// empty in that input, so the merged type keeps the typed side and
	// adds "null".
	if len(a) == 0 {
		return appendNull(b)
	}

	if len(b) == 0 {
		return appendNull(a)
	}

	coreA, nullA := splitNullType(a)
	coreB, nullB := splitNullType(b)

	var core []string

	switch {
	case len(coreA) == 0:
		core = coreB
	case len(coreB) == 0:
		core = coreA
	case sameStringSet(coreA, coreB):
		core = coreA
	case allNumeric(coreA) && allNumeric(coreB):
		core = []string{typeNumber}
	default:
		return nil
	}

	if nullA || nullB {
		core = append(slices.Clone(core), typeNull)
	}

	return core
}

// appendNull returns the type list with "null" appended. An already-nullable
// list is returned as-is, and appending clones first so the input's backing
// array is never mutated.
func appendNull(types []string) []string {
	if slices.Contains(types, typeNull) {
		return types
	}

	return append(slices.Clone(types), typeNull)
}

// splitNullType separates the "null" member from a type list, returning the
// remaining types and whether null was present.
func splitNullType(types []string) ([]string, bool) {
	var (
		core     []string
		nullable bool
	)

	for _, t := range types {
		if t == typeNull {
			nullable = true

			continue
		}

		core = append(core, t)
	}

	return core, nullable
}

// sameStringSet reports whether two slices contain the same elements,
// ignoring order.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for _, s := range a {
		if !slices.Contains(b, s) {
			return false
		}
	}

	return true
}

// allNumeric reports whether every type in the list is integer or number.
func allNumeric(types []string) bool {
	for _, t := range types {
		if t != typeInteger && t != typeNumber {
			return false
		}
	}

	return true
}

// isObjectType checks if a schema represents an object type via Types array.
func isObjectType(s *jsonschema.Schema) bool {
	return slices.Contains(s.Types, typeObject)
}

// isArrayType checks if a schema represents an array type via Types array.
func isArrayType(s *jsonschema.Schema) bool {
	return slices.Contains(s.Types, typeArray)
}
