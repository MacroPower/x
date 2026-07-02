package magicschema

import (
	"slices"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
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
	node, typ, known := resolveTagged(node)
	if known {
		// A known core tag on an empty scalar (e.g. "v: !!bool" with no value)
		// describes a null: the value is absent, and asserting the tagged type
		// would reject the null the source actually holds. Fall through to no
		// constraint (fail open) rather than emit a type the value fails.
		if isNullNode(unwrapNode(node)) {
			return ""
		}

		return typ
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
	}

	// Null and any unrecognized node carry no type constraint (fail open).
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

// resolveTagged follows AnchorNode wrappers and resolves TagNodes. On a known
// YAML tag it stops and returns that authoritative JSON Schema type with
// known=true; on an unknown tag or an anchor it follows .Value, which may
// bottom out at nil. When known is false the returned node is the underlying
// concrete node (possibly nil), so callers can finish their own classification.
func resolveTagged(node ast.Node) (ast.Node, string, bool) {
	for {
		switch n := node.(type) {
		case *ast.AnchorNode:
			node = n.Value
		case *ast.TagNode:
			if t, ok := tagType(n.Start.Value); ok {
				return node, t, true
			}

			node = n.Value

		default:
			return node, "", false
		}
	}
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

// extractComment extracts a plain-text description from a node's comments,
// skipping lines the isAnnotation predicate recognizes as annotation markers.
// Returns empty string if no suitable comment is found.
func extractComment(node ast.Node, isAnnotation func(string) bool) string {
	mvn, ok := node.(*ast.MappingValueNode)
	if !ok {
		return ""
	}

	// Head comment block above the key, narrowed to the run that
	// physically documents it. The run may begin inside a @schema block whose
	// opening fence was discarded with an earlier run, so the run's starting
	// fence state travels with it.
	if run, inSchema, inRoot := adjacentCommentRun(mvn.GetComment(), mvn.Key); len(run) > 0 {
		if desc := commentDescription(strings.Join(run, "\n"), inSchema, inRoot, isAnnotation); desc != "" {
			return desc
		}
	}

	// Try inline comment on the value node. Skip a sequence value: goccy stows
	// the first element's head comment on the SequenceNode itself, so reading it
	// here would leak an element's documentation as the array's description (a
	// comment above a later element does not leak, only the first's).
	if mvn.Value != nil {
		if _, isSeq := unwrapNode(mvn.Value).(*ast.SequenceNode); !isSeq {
			if desc := extractFromComment(mvn.Value.GetComment(), isAnnotation); desc != "" {
				return desc
			}
		}
	}

	// Try inline comment on the key node. MapKeyNode embeds ast.Node, so
	// GetComment is callable directly; the nil guard is the only protection
	// the old comma-ok assertion provided.
	if mvn.Key != nil {
		if desc := extractFromComment(mvn.Key.GetComment(), isAnnotation); desc != "" {
			return desc
		}
	}

	return ""
}

// extractFromComment extracts a description from a comment group node. An
// inline comment is a self-contained group, so it starts outside any block.
func extractFromComment(comment *ast.CommentGroupNode, isAnnotation func(string) bool) string {
	if comment == nil {
		return ""
	}

	return commentDescription(comment.String(), false, false, isAnnotation)
}

// commentDescription cleans a comment string and rejects results that are
// annotation markers rather than prose. The inSchema and inRoot flags seed the
// @schema / @schema.root fence state for callers handing over a run that
// already begins inside a block.
func commentDescription(s string, inSchema, inRoot bool, isAnnotation func(string) bool) string {
	desc := cleanComment(s, inSchema, inRoot, isAnnotation)
	if desc != "" && !isAnnotation(desc) {
		return desc
	}

	return ""
}

// HeadCommentRun returns the lines of the head comment run that physically
// documents a mapping value node's key, each line "#"-prefixed to match
// [ast.CommentGroupNode.String] output. It returns nil when node is not an
// *ast.MappingValueNode, the node has no head comment, or no run touches the
// key.
//
// The goccy parser merges separate comment blocks above a key -- a file
// header, a commented-out example for the previous key -- into one head
// comment group, erasing the physical blank lines between them, so reading
// the group whole attributes stale blocks to the key. Comment token positions
// reconstruct the layout: a run is a maximal sequence of comment tokens on
// consecutive lines at the same column, so a jump in line numbers is an
// erased blank line and a column change is a comment from a different nesting
// level (such as a commented-out child of the previous key). A "#"-only line
// is a paragraph separator within one run: it neither ends the run nor moves
// the column baseline. Only the final run counts, and only when its last line
// sits directly above the key and is not indented past the key's column;
// anything else is a stray block that documents nothing here. When a comment
// token or the key carries no position information the layout cannot be
// reconstructed, so the whole group is attributed to the key (fail open).
//
// The kept run may begin partway through a @schema or @schema.root block
// whose opening fence sat in a discarded earlier run. The two boolean results
// report the @schema and @schema.root fence state at the run's first line, so
// a caller interpreting block fences can tell orphaned block content from
// prose instead of mistaking the block's closing fence for an opener.
//
// The structural description fallback applies the same narrowing, so an
// annotator that scopes its annotations with HeadCommentRun agrees with the
// core on which comment block documents a key.
func HeadCommentRun(node ast.Node) ([]string, bool, bool) {
	mvn, ok := node.(*ast.MappingValueNode)
	if !ok || mvn == nil {
		return nil, false, false
	}

	return adjacentCommentRun(mvn.GetComment(), mvn.Key)
}

// adjacentCommentRun narrows a head comment group to the run of comment lines
// physically adjacent to key, implementing the contract documented on
// [HeadCommentRun] for an explicit comment group and key. The returned
// booleans report the @schema / @schema.root fence state at the run's first
// line, replayed from the discarded prefix, so [cleanComment] starts inside
// an already-open block and does not emit the orphaned block content as a
// description.
func adjacentCommentRun(comment *ast.CommentGroupNode, key ast.MapKeyNode) ([]string, bool, bool) {
	if comment == nil {
		return nil, false, false
	}

	var (
		all, run          []string
		prevLine, prevCol int
		unpositioned      bool
	)

	for _, c := range comment.Comments {
		tok := c.GetToken()
		if tok == nil {
			return nil, false, false
		}

		line := "#" + tok.Value
		all = append(all, line)

		if tok.Position == nil {
			unpositioned = true

			continue
		}

		// A "#"-only line is a paragraph separator within one description, not
		// a comment from a different nesting level, so its column carries no
		// meaning: it must neither reset the run on a column change nor move
		// the column baseline the next prose line is compared against. Only a
		// line gap (an erased blank line) still ends the run.
		if strings.TrimSpace(tok.Value) == "" {
			if len(run) > 0 && tok.Position.Line != prevLine+1 {
				run = run[:0]
			}

			run = append(run, line)
			prevLine = tok.Position.Line

			continue
		}

		if len(run) > 0 && (tok.Position.Line != prevLine+1 || tok.Position.Column != prevCol) {
			run = run[:0]
		}

		run = append(run, line)
		prevLine, prevCol = tok.Position.Line, tok.Position.Column
	}

	// A nil key cannot be placed, the same as a key token without position
	// information: the whole group is attributed (fail open).
	var keyTok *token.Token

	if key != nil {
		keyTok = key.GetToken()
	}

	if unpositioned || keyTok == nil || keyTok.Position == nil {
		return all, false, false
	}

	if len(run) == 0 || prevLine != keyTok.Position.Line-1 || prevCol > keyTok.Position.Column {
		return nil, false, false
	}

	// The run is the trailing suffix of all; replay the discarded prefix's
	// fence toggles so the run starts with the correct @schema / @schema.root
	// block state.
	inSchema, inRoot := false, false
	for _, line := range all[:len(all)-len(run)] {
		inSchema, inRoot, _ = schemaFenceState(strings.TrimSpace(StripCommentMarker(line)), inSchema, inRoot)
	}

	return run, inSchema, inRoot
}

// schemaFenceState applies the @schema / @schema.root block-fence toggles for
// one already-stripped, trimmed comment line and reports whether the line is a
// @schema or @schema.root marker (delimiter or inline form, neither of which
// carries description text). [ClassifySchemaLine] supplies the delimiter
// grammar; this function adds the streaming transitions: a root delimiter
// inside an open @schema block is junk content, not a toggle, and a @schema
// delimiter also ends an unclosed @schema.root block, mirroring the dadav
// annotator's comment scan. (That scan sees the whole comment and restores the
// root marker's delimiter role when the enclosing @schema block is unclosed;
// the distinction cannot surface here, where a line inside either block is
// equally not prose.) It returns the updated (inSchema, inRoot) state and
// whether the line was a marker.
func schemaFenceState(cleaned string, inSchema, inRoot bool) (bool, bool, bool) {
	switch ClassifySchemaLine(cleaned) {
	case SchemaLineRoot:
		if !inSchema {
			inRoot = !inRoot
		}

		return inSchema, inRoot, true

	case SchemaLineSchema:
		inRoot = false
		inSchema = !inSchema

		return inSchema, inRoot, true

	case SchemaLineInline:
		return inSchema, inRoot, true

	case SchemaLinePlain:
	}

	return inSchema, inRoot, false
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
// the isAnnotation predicate are markers. The generator's predicate combines
// the built-in [IsAnnotationComment] list with the [MarkerAnnotator]
// recognizers of the prepared annotators.
func cleanComment(s string, inSchemaBlock, inRootBlock bool, isAnnotation func(string) bool) string {
	var lines []string

	for line := range strings.SplitSeq(s, "\n") {
		// Markers are matched on a fully trimmed copy; the content keeps
		// its indentation. Fence markers are matched on a two-hash-capped
		// strip (see [StripCommentMarker]) so a "### @schema" line, which the
		// dadav annotator does not treat as a delimiter, is likewise not a
		// fence here; the content uses the all-hash [stripCommentPrefix] so
		// decorative hash banners still collapse to blank lines.
		content := stripCommentPrefix(line)
		cleaned := strings.TrimSpace(content)
		marker := strings.TrimSpace(StripCommentMarker(line))

		// Track helm-schema block fences so block content never leaks into
		// descriptions (see [schemaFenceState]).
		var fence bool

		inSchemaBlock, inRootBlock, fence = schemaFenceState(marker, inSchemaBlock, inRootBlock)
		if fence || inSchemaBlock || inRootBlock {
			continue
		}

		// Skip annotation markers from any recognized annotator format so
		// they never leak into descriptions. Blank lines stay: they
		// separate paragraphs.
		if cleaned != "" && isAnnotation(cleaned) {
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
	line = strings.TrimLeft(line, "#")
	line = strings.TrimPrefix(line, " ")

	return line
}

// IsAnnotationComment returns true if the comment looks like an annotation
// marker from any of the built-in annotator formats. Recognition is
// independent of which annotators are enabled, so a known format's marker
// never leaks into a description even when its annotator is off (fail open).
// Custom annotators extend marker recognition by implementing the optional
// [MarkerAnnotator] interface.
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
// "key.path -- description" where a key path precedes the " -- " separator.
// This prevents these comments from leaking as descriptions on parent nodes
// during fallback comment extraction.
func isHelmDocsOldStyleComment(s string) bool {
	idx := strings.Index(s, " -- ")
	if idx <= 0 {
		return false
	}

	return IsHelmDocsKeyPath(strings.TrimSpace(s[:idx]))
}

// IsHelmDocsKeyPath reports whether s is the key path of an old-style
// helm-docs "# key.path -- description" comment: a single non-empty token (no
// whitespace) of dot-separated, non-empty segments, none beginning with a
// digit. Upstream helm-docs accepts any non-empty key, so a dotless top-level
// key such as "replicas" qualifies alongside a dotted path like "image.tag".
// The segment rules reject text that cannot be a key reference -- the
// abbreviations "e.g."/"i.e."/"etc." leave a trailing empty segment, and a
// version like "v1.2" leaves a digit-led segment -- but a single prose word
// before " -- " is indistinguishable from a top-level key and counts as one,
// the cost of matching the upstream scanner. The norwoodj annotator and the
// fallback comment extractor share this one predicate so the file scan that
// records an old-style description and the structural pass that skips it
// cannot disagree and attribute the same comment to two unrelated keys.
func IsHelmDocsKeyPath(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t") {
		return false
	}

	for seg := range strings.SplitSeq(s, ".") {
		if seg == "" {
			return false
		}

		if seg[0] >= '0' && seg[0] <= '9' {
			return false
		}
	}

	return true
}

// inferItemsSchema creates an items schema from a sequence's elements. The
// values arrive alias-resolved but still tag-wrapped, so explicit tags reach
// inferType. Mixed element types widen, and a null or empty element among typed
// elements adds "null" to the items type so the source list validates.
// Returns nil (no constraint) for empty sequences, all-null sequences, and
// incompatible element types.
func inferItemsSchema(values []ast.Node) *jsonschema.Schema {
	if len(values) == 0 {
		return nil
	}

	var (
		resultType string
		hasNull    bool
	)

	// Genuine nulls mark the list nullable; unknown nodes stay transparent
	// via the empty string, which widens to the other side.
	for _, val := range values {
		if isNullNode(val) {
			hasNull = true

			continue
		}

		t := inferType(val)
		if t == "" {
			// An unknown node carries no type evidence; stay transparent.
			continue
		}

		if resultType == "" {
			resultType = t

			continue
		}

		// An empty widenType result means the two known types are
		// incompatible. Letting resultType absorb that would lose the
		// incompatibility the moment a later element matched an earlier type, so
		// an incompatible element sandwiched between compatible ones must drop
		// the constraint outright (fail open) rather than be silently forgotten.
		widened := widenType(resultType, t)
		if widened == "" {
			return nil
		}

		resultType = widened
	}

	// No positive type evidence: fail open.
	if resultType == "" {
		return nil
	}

	if hasNull {
		return &jsonschema.Schema{Types: []string{resultType, typeNull}}
	}

	return &jsonschema.Schema{Type: resultType}
}

// isNullNode reports whether a node is a YAML null value: a null literal or
// a !!null-tagged scalar, looking through anchor wrappers. A nil node counts
// as null: [resolveAliases] returns nil for an alias with no in-scope anchor,
// which the package treats as null, and anchor or tag wrappers can bottom out
// at a nil value the same way. Treating nil as null keeps a broken alias
// consistent with a genuine null everywhere inference gates on it -- item-list
// nullability and the all-mappings property-preserving merge alike.
func isNullNode(node ast.Node) bool {
	// A known tag is authoritative: !!null (the empty type) is a null, any
	// other core tag is not. Unknown tags and anchors fall through to the
	// underlying node, which may be nil for a broken alias.
	node, typ, known := resolveTagged(node)
	if known {
		return typ == ""
	}

	if node == nil {
		return true
	}

	_, ok := node.(*ast.NullNode)

	return ok
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

// appendNull returns the type list with "null" appended. Both branches clone,
// so the result never aliases the input's backing array -- widenTypeList must
// not hand back a slice still owned by an input schema's Types.
func appendNull(types []string) []string {
	if slices.Contains(types, typeNull) {
		return slices.Clone(types)
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

// sameStringSet reports whether two slices contain the same elements with the
// same multiplicity, ignoring order. Counting occurrences keeps a duplicated
// member from standing in for a missing one: ["string", "string"] and
// ["string", "integer"] are equal length but not the same multiset. Equal
// lengths plus no negative count after subtracting a means the multisets match.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	counts := make(map[string]int, len(b))
	for _, s := range b {
		counts[s]++
	}

	for _, s := range a {
		counts[s]--
		if counts[s] < 0 {
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

// hasType reports whether a schema constrains its instance to type t, via
// either the scalar Type field or the Types union.
func hasType(s *jsonschema.Schema, t string) bool {
	return s.Type == t || slices.Contains(s.Types, t)
}
