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

	commentStr := comment.String()

	// Parse @schema.root blocks only from the first key in the mapping.
	if !a.seenFirstKey {
		rootContent := extractSchemaRootBlock(commentStr)
		if rootContent != "" {
			a.parseRootBlock(rootContent)
		}
	}

	a.seenFirstKey = true

	// Parse @schema blocks.
	blockContent := extractSchemaBlock(commentStr)
	if blockContent == "" {
		// No @schema block found. Use non-annotation comment as description.
		return nil
	}

	return a.parseBlock(blockContent, commentStr)
}

// RootSchema returns the parsed root-level schema, if any.
func (a *Annotator) RootSchema() *jsonschema.Schema {
	return a.rootSchema
}

// parseBlock parses the content of a @schema block into an AnnotationResult.
func (a *Annotator) parseBlock(content, fullComment string) *magicschema.AnnotationResult {
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

	// Extract description from non-annotation comments if not set.
	if schema.Description == "" {
		desc := extractNonAnnotationDescription(fullComment)
		if desc != "" {
			schema.Description = desc
		}
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
		schema.Definitions = magicschema.ToSubSchemaMap(val)
	case "$defs":
		schema.Defs = magicschema.ToSubSchemaMap(val)
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
// comment line. The three block extractors share it so the grammar -- which
// bare markers delimit a block, and which @schema-prefixed lines are the inline
// form rather than a delimiter -- lives in one place; each extractor still
// applies its own block-state transitions. The "@schema.root" literal must be
// contiguous and bare: "@schema .root" with a space is the inline form, and
// trailing content after either marker is inline rather than a delimiter.
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

// extractSchemaBlock extracts the content between # @schema delimiters.
// Multiple @schema blocks in the same comment are concatenated (toggle behavior).
// Content inside @schema.root blocks is excluded.
func extractSchemaBlock(comment string) string {
	lines := strings.Split(comment, "\n")

	var (
		inBlock     bool
		inRootBlock bool
		content     []string
	)

	for _, line := range lines {
		// Strip once; markers match on a fully trimmed copy while content
		// keeps its indentation beyond the marker and single space.
		stripped := magicschema.StripCommentMarker(line)
		trimmed := strings.TrimSpace(stripped)

		switch classifySchemaLine(trimmed) {
		case schemaLineRoot:
			// Inside an open @schema block the marker is junk content, not a
			// delimiter: toggling there would swallow the rest of the block.
			if !inBlock {
				inRootBlock = !inRootBlock
			}

		case schemaLineSchema:
			// Toggle delimiter -- supports multiple blocks. A @schema delimiter
			// also ends an unclosed @schema.root block, so a missing root close
			// cannot swallow every following schema block.
			inRootBlock = false
			inBlock = !inBlock

		case schemaLineInline:
			// Not a delimiter (inline helm-values-schema form), never collected.

		default: // schemaLinePlain
			if inBlock && !inRootBlock {
				content = append(content, stripped)
			}
		}
	}

	if len(content) == 0 {
		return ""
	}

	return strings.Join(content, "\n")
}

// extractSchemaRootBlock extracts the content between @schema.root delimiters.
func extractSchemaRootBlock(comment string) string {
	lines := strings.Split(comment, "\n")

	var (
		inBlock       bool
		inSchemaBlock bool
		content       []string
	)

scan:
	for _, line := range lines {
		// Strip once; markers match on a fully trimmed copy while content
		// keeps its indentation beyond the marker and single space.
		stripped := magicschema.StripCommentMarker(line)
		trimmed := strings.TrimSpace(stripped)

		switch classifySchemaLine(trimmed) {
		case schemaLineRoot:
			// Inside an open @schema block the marker is junk content, not a
			// delimiter, mirroring extractSchemaBlock.
			if inSchemaBlock {
				continue
			}

			if inBlock {
				break scan // Closing delimiter -- only the first root block.
			}

			inBlock = true

		case schemaLineSchema:
			// A @schema delimiter ends an unclosed root block (root content
			// cannot extend into schema blocks) and otherwise toggles
			// schema-block state so root markers inside a schema block are
			// ignored as junk.
			if inBlock {
				break scan
			}

			inSchemaBlock = !inSchemaBlock

		case schemaLineInline:
			// An inline "@schema key:value" is not a delimiter, but it is still
			// skipped rather than appended: letting it land in the root YAML
			// would make goccy reject the whole block as invalid.

		default: // schemaLinePlain
			if inBlock {
				content = append(content, stripped)
			}
		}
	}

	if len(content) == 0 {
		return ""
	}

	return strings.Join(content, "\n")
}

// extractNonAnnotationDescription extracts description text from comments
// that are not part of @schema or @schema.root blocks. Lines join with
// newlines and keep their indentation beyond the comment marker and single
// following space, matching upstream helm-schema, so YAML snippets embedded
// in comments keep their structure.
func extractNonAnnotationDescription(comment string) string {
	lines := strings.Split(comment, "\n")

	var (
		descLines     []string
		inSchemaBlock bool
		inRootBlock   bool
	)

	for _, line := range lines {
		// Markers are matched on a fully trimmed copy; the content keeps
		// its indentation.
		content := magicschema.StripCommentMarker(line)
		stripped := strings.TrimSpace(content)

		switch classifySchemaLine(stripped) {
		case schemaLineRoot:
			// Inside an open @schema block the marker is junk content, not a
			// delimiter.
			if !inSchemaBlock {
				inRootBlock = !inRootBlock
			}

		case schemaLineSchema:
			// A @schema delimiter also ends an unclosed @schema.root block.
			inRootBlock = false
			inSchemaBlock = !inSchemaBlock

		case schemaLineInline:
			// Inline helm-values-schema content -- skipped, not a description.

		default: // schemaLinePlain
			if inSchemaBlock || inRootBlock {
				continue
			}

			// Regular comment line: strip the helm-docs "-- " prefix off the
			// indentation-preserving copy.
			cleaned := strings.TrimPrefix(content, "-- ")
			if magicschema.IsAnnotationComment(cleaned) {
				continue
			}

			descLines = append(descLines, cleaned)
		}
	}

	// Keep only the last comment group, ignoring trailing blank lines so a
	// blank final line cannot discard the whole description.
	group := magicschema.LastCommentGroup(descLines)
	if len(group) == 0 {
		return ""
	}

	return strings.Join(group, "\n")
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
			switch s := item.(type) {
			case string:
				types = append(types, s)
			case nil:
				// YAML null in a type array (e.g., [string, null]) is
				// deserialized as nil. Convert to the "null" type string,
				// matching upstream behavior.
				types = append(types, "null")
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
// either schema or string-array values.
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
