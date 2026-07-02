package dadav

import (
	"log/slog"
	"math"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/magicschema"
)

// Annotator parses # @schema / # @schema block annotations from
// the dadav/helm-schema project.
type Annotator struct {
	rootSchema   *jsonschema.Schema
	seenFirstKey bool
}

// New creates a new helm-schema block annotator.
func New() *Annotator {
	return &Annotator{}
}

// Name is the canonical annotator name, used as the registry key and in
// the --annotators flag.
const Name = "helm-schema"

// Name returns the annotator name.
func (a *Annotator) Name() string {
	return Name
}

// ForContent returns a new Annotator ready to process the given file.
func (a *Annotator) ForContent(_ []byte) (magicschema.Annotator, error) {
	return New(), nil
}

// Annotate extracts schema annotations from @schema blocks in comments.
func (a *Annotator) Annotate(node ast.Node, _ string) *magicschema.AnnotationResult {
	mvn, ok := node.(*ast.MappingValueNode)
	if !ok {
		return nil
	}

	comment := mvn.GetComment()
	if comment == nil {
		// Track that we've seen the first key even if it has no comments.
		a.seenFirstKey = true

		return nil
	}

	// Parse @schema.root blocks only from the first key in the mapping. Root
	// extraction reads the whole merged comment group: upstream parses the
	// first key's full head comment (and the document head comment) for root
	// blocks without stripping leading comment groups, so a root block
	// separated from the first key by a blank line still applies.
	if !a.seenFirstKey {
		if root := scanCommentBlocks(comment.String()).root; root != "" {
			a.parseRootBlock(root)
		}
	}

	a.seenFirstKey = true

	// Property-level parsing reads only the head-comment run that physically
	// documents the key. The parser merges blank-line-separated comment
	// blocks -- a file header, a stale annotation block for a removed key --
	// into one head comment group, and applying such a detached block here
	// can produce a schema the key's own value fails. Upstream strips
	// everything before the last blank line (leadingCommentsRemover) before
	// parsing @schema blocks and descriptions; [magicschema.HeadCommentRun]
	// reconstructs the same narrowing from comment token positions. The scan
	// starts outside any block, as upstream does after stripping, so a block
	// whose opening fence sits in a discarded run contributes nothing; the
	// fence-state results are therefore unused.
	run, _, _ := magicschema.HeadCommentRun(mvn)
	if len(run) == 0 {
		return nil
	}

	blocks := scanCommentBlocks(strings.Join(run, "\n"))
	if blocks.schema == "" {
		// No @schema block found. The generator's structural fallback uses
		// the non-annotation comment as the description.
		return nil
	}

	return a.parseBlock(blocks.schema, blocks.description)
}

// RootSchema returns the parsed root-level schema, if any.
func (a *Annotator) RootSchema() *jsonschema.Schema {
	return a.rootSchema
}

// parseBlock parses the content of a @schema block into an AnnotationResult.
// The description is the comment's non-annotation prose, applied when the
// block itself sets none.
func (a *Annotator) parseBlock(content, description string) *magicschema.AnnotationResult {
	var raw map[string]any

	err := yaml.Unmarshal([]byte(content), &raw)
	if err != nil {
		slog.Warn("parse @schema block", slog.Any("error", err))

		return nil
	}

	if len(raw) == 0 {
		return nil
	}

	schema := &jsonschema.Schema{}
	result := &magicschema.AnnotationResult{Schema: schema}

	for key, val := range raw {
		a.applyField(schema, result, key, val)
	}

	// Fall back to the non-annotation comment prose if the block sets no
	// description.
	if schema.Description == "" && description != "" {
		schema.Description = description
	}

	return result
}

// rootAllowedKeys is the subset of @schema fields a @schema.root block
// propagates to the document-level schema. Type-, value-, and structure-level
// keywords are intentionally excluded; x-* extensions are allowed by prefix
// (see rootAllowed).
var rootAllowedKeys = map[string]struct{}{
	"title":                {},
	"description":          {},
	"$ref":                 {},
	"examples":             {},
	"deprecated":           {},
	"readOnly":             {},
	"writeOnly":            {},
	"additionalProperties": {},
}

// rootAllowed reports whether a @schema.root key propagates to the
// document-level schema.
func rootAllowed(key string) bool {
	if _, ok := rootAllowedKeys[key]; ok {
		return true
	}

	return strings.HasPrefix(key, "x-")
}

// parseRootBlock parses @schema.root content into the root schema. Only the
// documented rootAllowed subset propagates; each allowed key is dispatched
// through applyField so its handling stays identical to a property-level
// @schema block rather than a second copy that can drift.
func (a *Annotator) parseRootBlock(content string) {
	var raw map[string]any

	err := yaml.Unmarshal([]byte(content), &raw)
	if err != nil {
		slog.Warn("parse @schema.root block", slog.Any("error", err))

		return
	}

	schema := &jsonschema.Schema{}

	// A throwaway result captures applyField's required/skip signals, which no
	// rootAllowed key sets, so root blocks contribute none.
	result := &magicschema.AnnotationResult{Schema: schema}

	for key, val := range raw {
		if rootAllowed(key) {
			a.applyField(schema, result, key, val)
		}
	}

	a.rootSchema = schema
}

// applyField applies a single key-value pair from a @schema block to the schema.
//
//nolint:cyclop,gocyclo,goconst // dispatching all JSON Schema fields requires many cases and repeated field names
func (a *Annotator) applyField(schema *jsonschema.Schema, result *magicschema.AnnotationResult, key string, val any) {
	switch key {
	case "type":
		applyType(schema, val)
	case "title":
		schema.Title = toString(val)
	case "description":
		schema.Description = toString(val)
	case "default":
		schema.Default = magicschema.DefaultValue(val)
	case "enum":
		if arr, ok := val.([]any); ok {
			schema.Enum = magicschema.FilterJSONSafe(arr)
		}

	case "const":
		schema.Const = magicschema.ConstValue(val)
	case "pattern":
		schema.Pattern = toString(val)
	case "format":
		schema.Format = toString(val)
	case "minimum":
		schema.Minimum = toFloat64Ptr(val)
	case "maximum":
		schema.Maximum = toFloat64Ptr(val)
	case "exclusiveMinimum":
		schema.ExclusiveMinimum = toFloat64Ptr(val)
	case "exclusiveMaximum":
		schema.ExclusiveMaximum = toFloat64Ptr(val)
	case "multipleOf":
		schema.MultipleOf = toFloat64Ptr(val)
	case "minLength":
		schema.MinLength = toIntPtr(val)
	case "maxLength":
		schema.MaxLength = toIntPtr(val)
	case "minItems":
		schema.MinItems = toIntPtr(val)
	case "maxItems":
		schema.MaxItems = toIntPtr(val)
	case "uniqueItems":
		schema.UniqueItems = toBool(val)
	case "minProperties":
		schema.MinProperties = toIntPtr(val)
	case "maxProperties":
		schema.MaxProperties = toIntPtr(val)
	case "required":
		applyRequired(schema, result, val)
	case "deprecated":
		schema.Deprecated = toBool(val)
	case "readOnly":
		schema.ReadOnly = toBool(val)
	case "writeOnly":
		schema.WriteOnly = toBool(val)
	case "examples":
		if arr, ok := val.([]any); ok {
			schema.Examples = magicschema.FilterJSONSafe(arr)
		}

	case "items":
		// Draft 7 allows items as a single schema (constrains every element) or
		// an array of schemas (tuple validation). A []any is the tuple form, so
		// route it to ItemsArray, which marshals to "items": [...]. ToSubSchema
		// only decodes a single object schema and would drop the tuple, leaving
		// items unset while a sibling additionalItems survives -- an
		// over-restrictive, fail-closed schema.
		if _, ok := val.([]any); ok {
			schema.ItemsArray = magicschema.ToSubSchemaArray(val)
		} else {
			schema.Items = magicschema.ToSubSchema(val)
		}

	case "anyOf":
		schema.AnyOf = magicschema.ToSubSchemaArray(val)
	case "oneOf":
		schema.OneOf = magicschema.ToSubSchemaArray(val)
	case "allOf":
		schema.AllOf = magicschema.ToSubSchemaArray(val)
	case "not":
		schema.Not = magicschema.ToSubSchema(val)
	case "if":
		schema.If = magicschema.ToSubSchema(val)
	case "then":
		schema.Then = magicschema.ToSubSchema(val)
	case "else":
		schema.Else = magicschema.ToSubSchema(val)
	case "additionalProperties":
		schema.AdditionalProperties = toAdditionalProperties(val)
	case "properties":
		schema.Properties = magicschema.ToSubSchemaMap(val)
	case "patternProperties":
		schema.PatternProperties = magicschema.ToSubSchemaMap(val)
	case "propertyNames":
		schema.PropertyNames = magicschema.ToSubSchema(val)
	case "contains":
		schema.Contains = magicschema.ToSubSchema(val)
	case "additionalItems":
		schema.AdditionalItems = toAdditionalProperties(val)
	case "dependencies":
		applyDependencies(schema, val)
	case "definitions":
		// The definitions and $defs keys are the Draft 7 and 2019-09+
		// spellings of the same keyword, and the jsonschema marshaler rejects
		// a schema carrying both, which would break the whole document's
		// final marshal (the same invariant SetSchemaType guards for
		// Type/Types). Block keys arrive in map order, so exclusivity uses a
		// fixed, order-independent precedence: definitions wins when one
		// block carries both spellings, matching the package's Draft 7 output
		// and the upstream's preference for definitions. Each side yields
		// only to a value that parses, so an unparseable winner cannot drop a
		// valid loser.
		if defs := magicschema.ToSubSchemaMap(val); defs != nil {
			schema.Definitions = defs
			schema.Defs = nil
		}

	case "$defs":
		if schema.Definitions == nil {
			schema.Defs = magicschema.ToSubSchemaMap(val)
		}

	case "$ref":
		schema.Ref = toString(val)
	case "$id":
		schema.ID = toString(val)
	case "$comment":
		schema.Comment = toString(val)
	case "contentEncoding":
		schema.ContentEncoding = toString(val)
	case "contentMediaType":
		schema.ContentMediaType = toString(val)
	default:
		if strings.HasPrefix(key, "x-") {
			setExtra(schema, key, val)
		}
	}
}

// setExtra stores a custom "x-" prefixed keyword in the schema's Extra map,
// lazily allocating the map on first use. Shared by parseRootBlock and
// applyField.
func setExtra(schema *jsonschema.Schema, key string, val any) {
	// A non-finite float (NaN/Inf) anywhere in val cannot be marshaled to JSON
	// and would break the whole document's final marshal, so drop the key
	// rather than store an unmarshalable value -- matching ConstValue and the
	// numeric-bound guards (toFloat64Ptr, toIntPtr). The marshal probe rejects
	// the value at any nesting depth, so an x- annotation carrying a non-finite
	// float, even nested inside a list or map, is skipped entirely (fail open).
	if magicschema.DefaultValue(val) == nil {
		return
	}

	if schema.Extra == nil {
		schema.Extra = make(map[string]any)
	}

	schema.Extra[key] = val
}

// schemaLineKind classifies a comment line's role in the @schema/@schema.root
// delimiter grammar, after the comment marker is stripped and the remainder
// fully trimmed.
type schemaLineKind int

const (
	// A line that is not a @schema or @schema.root marker.
	schemaLinePlain schemaLineKind = iota
	// A bare "@schema.root" block delimiter.
	schemaLineRoot
	// A bare "@schema" block delimiter, including a junk suffix such as
	// "@schema@" that upstream still treats as a delimiter.
	schemaLineSchema
	// A @schema- or @schema.root-prefixed line that is not a delimiter --
	// trailing content or the whitespace-separated helm-values-schema inline
	// form -- so it is never collected as block content.
	schemaLineInline
)

// classifySchemaLine reports the delimiter role of a stripped, fully trimmed
// comment line, so the grammar -- which bare markers delimit a block, and
// which @schema-prefixed lines are the inline form rather than a delimiter --
// lives in one place; [scanCommentBlocks] applies the block-state
// transitions. The "@schema.root" literal must be contiguous and bare:
// "@schema .root" with a space is the inline form, and trailing content after
// either marker is inline rather than a delimiter.
func classifySchemaLine(trimmed string) schemaLineKind {
	if after, ok := strings.CutPrefix(trimmed, "@schema.root"); ok {
		if strings.TrimSpace(after) == "" {
			return schemaLineRoot
		}

		return schemaLineInline
	}

	if after, ok := strings.CutPrefix(trimmed, "@schema"); ok {
		if isDelimiterSuffix(after) {
			return schemaLineSchema
		}

		return schemaLineInline
	}

	return schemaLinePlain
}

// commentBlocks is the classified content of one comment scan: the
// concatenated @schema block content, the first @schema.root block's content,
// and the non-annotation description prose.
type commentBlocks struct {
	schema      string
	root        string
	description string
}

// scanCommentBlocks classifies every comment line as @schema block content,
// @schema.root block content, or description prose in a single pass, so the
// delimiter grammar and its block-state transitions live in one place.
//
// Block semantics:
//
//   - @schema delimiters toggle, so multiple blocks on the same key are
//     concatenated. An unclosed @schema block is processed best-effort:
//     content up to the end of the comment is included, except the lines a
//     following @schema.root block claims.
//   - Only the first @schema.root block applies; later root blocks still
//     toggle state so their content stays out of the schema content and the
//     description. A @schema delimiter closes an open root block (root
//     content cannot extend into schema blocks), so a missing root close
//     cannot swallow every following schema block. A root block still open
//     when the comment ends is unclosed and silently ignored.
//   - A bare @schema.root marker inside a closed @schema block is junk
//     content, not a delimiter: toggling there would swallow the rest of the
//     block. Inside an unclosed @schema block the marker keeps its delimiter
//     role, so a root block following a missing @schema close parses as a
//     root block instead of leaking into the property schema -- matching the
//     upstream's root pass, which strips root blocks before schema parsing.
//
// Block content and description lines keep their indentation beyond the
// comment marker and single following space, matching upstream helm-schema,
// so YAML snippets embedded in comments keep their structure. Description
// lines are additionally stripped of the helm-docs "-- " prefix, and lines
// that are themselves annotation markers are skipped.
func scanCommentBlocks(comment string) commentBlocks {
	lines := strings.Split(comment, "\n")

	// Classify every line up front: the root-marker transition depends on
	// whether the enclosing @schema block is closed by a later delimiter,
	// which needs the total delimiter count before the stateful pass runs.
	stripped := make([]string, len(lines))
	kinds := make([]schemaLineKind, len(lines))

	var fences int

	for i, line := range lines {
		// Strip once; markers match on a fully trimmed copy while content
		// keeps its indentation beyond the marker and single space.
		stripped[i] = magicschema.StripCommentMarker(line)
		kinds[i] = classifySchemaLine(strings.TrimSpace(stripped[i]))

		if kinds[i] == schemaLineSchema {
			fences++
		}
	}

	var (
		inSchema, inRoot, rootDone bool
		seen                       int

		schemaLines, rootLines, descLines []string
	)

	for i, kind := range kinds {
		switch kind {
		case schemaLineRoot:
			// Junk inside a closed @schema block (see semantics above): seen
			// counting below fences means another @schema delimiter follows,
			// so the open block is eventually closed.
			if inSchema && seen < fences {
				continue
			}

			if inRoot {
				rootDone = true // Closing delimiter -- only the first root block.
			}

			inRoot = !inRoot

		case schemaLineSchema:
			// Toggle delimiter -- supports multiple concatenated blocks --
			// that also closes an open @schema.root block.
			if inRoot {
				rootDone = true
				inRoot = false
			}

			seen++
			inSchema = !inSchema

		case schemaLineInline:
			// Not a delimiter (inline helm-values-schema form) and never
			// collected: letting it land in block YAML would make goccy
			// reject the whole block, and it is not a description either.

		default: // schemaLinePlain
			switch {
			case inRoot:
				if !rootDone {
					rootLines = append(rootLines, stripped[i])
				}

			case inSchema:
				schemaLines = append(schemaLines, stripped[i])

			default:
				// Regular comment line: strip the helm-docs "-- " prefix off
				// the indentation-preserving copy.
				cleaned := strings.TrimPrefix(stripped[i], "-- ")
				if magicschema.IsAnnotationComment(cleaned) {
					continue
				}

				descLines = append(descLines, cleaned)
			}
		}
	}

	// A root block still open at the end of the comment is unclosed and is
	// silently ignored. Content collected before rootDone belongs to a block
	// a delimiter closed and is kept.
	if inRoot && !rootDone {
		rootLines = nil
	}

	// The caller passes the head-comment run that documents the key, which
	// is already one comment group. A "#"-only line inside it (an empty
	// string after marker stripping) is a paragraph separator kept as a
	// blank line in the joined description, matching the upstream join;
	// trimming the group's blank edges keeps the description from starting
	// or ending with a separator.
	descLines = magicschema.LastCommentGroup(descLines)

	return commentBlocks{
		schema:      strings.Join(schemaLines, "\n"),
		root:        strings.Join(rootLines, "\n"),
		description: strings.Join(descLines, "\n"),
	}
}

// isDelimiterSuffix reports whether the text following the "@schema" token
// forms a block delimiter. Upstream helm-schema toggles blocks on any line
// prefixed with "# @schema", so junk suffixes such as "@schema@" (seen in
// the wild in cilium's values.yaml) still delimit a block. A
// whitespace-separated suffix is excluded because that form is the
// helm-values-schema inline annotation.
func isDelimiterSuffix(after string) bool {
	return after == "" || (after[0] != ' ' && after[0] != '\t')
}

// applyType sets Type or Types on the schema from a YAML value.
func applyType(schema *jsonschema.Schema, val any) {
	switch v := val.(type) {
	case string:
		schema.Type = v
	case []any:
		types := make([]string, 0, len(v))

		for _, item := range v {
			switch item := item.(type) {
			case string:
				// An empty string is not a valid JSON Schema type token;
				// SetSchemaType rewrites it to the "null" type.
				types = append(types, item)

			case nil:
				// YAML null in a type array (e.g., [string, null]) is
				// deserialized as nil. Convert to the "null" type string,
				// matching upstream behavior.
				types = append(types, "null")
			default:
				// A member that is not a string or null cannot be a JSON Schema
				// type token (e.g. type: [string, 1]). Narrowing to the
				// representable members would drop a type the value may take and
				// reject it, so drop the type entirely and fall back to value
				// inference (fail open).
				return
			}
		}

		// SetSchemaType collapses a single type to the scalar Type and leaves an
		// empty list (e.g. type: [], type: [1, 2]) untouched so structural
		// inference and the fail-open default still apply.
		magicschema.SetSchemaType(schema, types)
	}
}

// applyRequired handles the "required" field which can be either a bool
// (HasRequired) or an array of strings (Required on the schema).
func applyRequired(schema *jsonschema.Schema, result *magicschema.AnnotationResult, val any) {
	switch v := val.(type) {
	case bool:
		result.HasRequired = &v
	case []any:
		strs := make([]string, 0, len(v))

		for _, item := range v {
			if s, ok := item.(string); ok {
				strs = append(strs, s)
			}
		}

		schema.Required = strs
	}
}

// applyDependencies handles the "dependencies" field which can contain
// either schema or string-array values. Each key routes to exactly one of
// DependencySchemas or DependencyStrings based on its value's shape, so the
// jsonschema marshaler's invariant that no key appears in both maps holds by
// construction.
func applyDependencies(schema *jsonschema.Schema, val any) {
	m, ok := val.(map[string]any)
	if !ok {
		return
	}

	for key, depVal := range m {
		switch dv := depVal.(type) {
		case []any:
			if schema.DependencyStrings == nil {
				schema.DependencyStrings = make(map[string][]string)
			}

			strs := make([]string, 0, len(dv))

			for _, item := range dv {
				if s, ok := item.(string); ok {
					strs = append(strs, s)
				}
			}

			schema.DependencyStrings[key] = strs

		case map[string]any:
			// Drop a dependency whose schema cannot round-trip rather than
			// store a nil that marshals to an invalid "key": null, matching the
			// nil-guard in ToSubSchemaMap.
			if s := magicschema.ToSubSchema(dv); s != nil {
				if schema.DependencySchemas == nil {
					schema.DependencySchemas = make(map[string]*jsonschema.Schema)
				}

				schema.DependencySchemas[key] = s
			}
		}
	}
}

// toAdditionalProperties converts a value to an additionalProperties or
// additionalItems schema. Upstream supports both boolean and schema values
// for either field.
func toAdditionalProperties(val any) *jsonschema.Schema {
	switch v := val.(type) {
	case bool:
		if v {
			return magicschema.TrueSchema()
		}

		return magicschema.FalseSchema()

	case map[string]any:
		return magicschema.ToSubSchema(v)
	}

	return nil
}

// toString converts a value to a string.
func toString(val any) string {
	if s, ok := val.(string); ok {
		return s
	}

	return ""
}

// toBool converts a value to a bool.
func toBool(val any) bool {
	if b, ok := val.(bool); ok {
		return b
	}

	return false
}

// toFloat64Ptr converts a numeric value to *float64. Non-finite floats
// (Inf/NaN, which YAML accepts but JSON cannot represent) are rejected: one
// reaching a schema field would break the whole document's final json.Marshal.
func toFloat64Ptr(val any) *float64 {
	switch v := val.(type) {
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return nil
		}

		return &v

	case int:
		f := float64(v)

		return &f

	case int64:
		f := float64(v)

		return &f

	case uint64:
		f := float64(v)

		return &f
	}

	return nil
}

// toIntPtr converts a numeric value to *int. Values outside the int range are
// rejected rather than wrapped: goccy decodes integers above MaxInt64 as
// uint64, and a bare int(v) cast would turn a large positive bound into a
// negative one. Non-integral or non-finite floats are likewise dropped.
func toIntPtr(val any) *int {
	switch v := val.(type) {
	case int:
		return &v
	case int64:
		if v < math.MinInt || v > math.MaxInt {
			return nil
		}

		i := int(v) //nolint:gosec // bounded by the MinInt/MaxInt check above

		return &i

	case uint64:
		if v > math.MaxInt {
			return nil
		}

		i := int(v) //nolint:gosec // bounded by the MaxInt check above

		return &i

	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) || v != math.Trunc(v) ||
			v < math.MinInt || v > math.MaxInt {
			return nil
		}

		i := int(v)

		return &i
	}

	return nil
}
