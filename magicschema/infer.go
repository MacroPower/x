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

	// Try head comment on the MappingValueNode itself.
	if desc := extractFromComment(mvn.GetComment()); desc != "" {
		return desc
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

	desc := cleanComment(comment.String())
	if desc != "" && !IsAnnotationComment(desc) {
		return desc
	}

	return ""
}

// cleanComment strips comment markers and whitespace from a comment string.
// Multi-line comments are joined with spaces, using only the last comment
// group (the lines after the last blank line, ignoring trailing blanks).
func cleanComment(s string) string {
	var parts []string

	for _, line := range LastCommentGroup(strings.Split(s, "\n")) {
		cleaned := strings.TrimSpace(stripCommentPrefix(line))
		if cleaned == "" {
			continue
		}

		// Skip annotation markers from any supported annotator format so
		// they never leak into descriptions.
		if IsAnnotationComment(cleaned) {
			continue
		}

		parts = append(parts, cleaned)
	}

	return strings.Join(parts, " ")
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
// If elements have mixed types, the type is widened. Returns nil for empty
// sequences.
func inferItemsSchema(seq *ast.SequenceNode) *jsonschema.Schema {
	if len(seq.Values) == 0 {
		return nil
	}

	var resultType string

	first := true

	for _, val := range seq.Values {
		elemType := inferType(val)
		if first {
			resultType = elemType
			first = false

			continue
		}

		resultType = widenType(resultType, elemType)
	}

	if resultType == "" {
		return nil
	}

	return &jsonschema.Schema{Type: resultType}
}

// widenType returns the widened type when merging two type strings.
// Returns empty string (no constraint) for incompatible types.
func widenType(a, b string) string {
	if a == b {
		return a
	}

	// Null/empty merges transparently.
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
// unions. An empty list (no constraint) adopts the other side, matching the
// "null means the value was empty in one file" rule, and the "null" member
// carries through whenever either side allows null. Beyond that, identical
// sets merge, all-numeric sets widen to number, and anything else drops the
// constraint entirely (fail open).
func widenTypeList(a, b []string) []string {
	if len(a) == 0 {
		return b
	}

	if len(b) == 0 {
		return a
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
