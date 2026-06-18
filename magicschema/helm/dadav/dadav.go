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

// parseRootBlock parses @schema.root content into the root schema.
//
//nolint:goconst // JSON Schema field names repeated intentionally across switch cases
func (a *Annotator) parseRootBlock(content string) {
	var raw map[string]any

	err := yaml.Unmarshal([]byte(content), &raw)
	if err != nil {
		slog.Warn("parse @schema.root block", slog.Any("error", err))

		return
	}

	schema := &jsonschema.Schema{}

	// Only propagate a subset of fields from root blocks.
	for key, val := range raw {
		switch key {
		case "title":
			schema.Title = toString(val)
		case "description":
			schema.Description = toString(val)
		case "$ref":
			schema.Ref = toString(val)
		case "examples":
			if arr, ok := val.([]any); ok {
				schema.Examples = magicschema.FilterJSONSafe(arr)
			}

		case "deprecated":
			schema.Deprecated = toBool(val)
		case "readOnly":
			schema.ReadOnly = toBool(val)
		case "writeOnly":
			schema.WriteOnly = toBool(val)
		case "additionalProperties":
			schema.AdditionalProperties = toAdditionalProperties(val)
		default:
			if strings.HasPrefix(key, "x-") {
				if schema.Extra == nil {
					schema.Extra = make(map[string]any)
				}

				schema.Extra[key] = val
			}
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
		schema.Items = magicschema.ToSubSchema(val)
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
			if schema.Extra == nil {
				schema.Extra = make(map[string]any)
			}

			schema.Extra[key] = val
		}
	}
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
		stripped := stripCommentHash(line)
		trimmed := strings.TrimSpace(stripped)

		// Check for @schema.root delimiter (toggle root block state).
		// Lines with trailing content are not delimiters and are skipped.
		// Inside an open @schema block the marker is junk content, not a
		// delimiter: toggling there would swallow the rest of the block.
		if after, ok := strings.CutPrefix(trimmed, "@schema.root"); ok {
			if !inBlock && strings.TrimSpace(after) == "" {
				inRootBlock = !inRootBlock
			}

			continue
		}

		if after, ok := strings.CutPrefix(trimmed, "@schema"); ok {
			if isDelimiterSuffix(after) {
				// Toggle delimiter -- supports multiple blocks. A
				// @schema delimiter also ends an unclosed @schema.root
				// block, so a missing root close cannot swallow every
				// following schema block.
				inRootBlock = false
				inBlock = !inBlock
			}

			// Otherwise @schema with whitespace-separated content on the
			// same line -- not this annotator (that's helm-values-schema
			// inline format).
			continue
		}

		// Skip lines inside @schema.root blocks.
		if inRootBlock {
			continue
		}

		if inBlock {
			content = append(content, stripped)
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

	for _, line := range lines {
		// Strip once; markers match on a fully trimmed copy while content
		// keeps its indentation beyond the marker and single space.
		stripped := stripCommentHash(line)
		trimmed := strings.TrimSpace(stripped)

		if after, ok := strings.CutPrefix(trimmed, "@schema.root"); ok {
			rest := strings.TrimSpace(after)
			if rest != "" {
				// @schema.root with trailing content -- skip.
				continue
			}

			// Inside an open @schema block the marker is junk content,
			// not a delimiter, mirroring extractSchemaBlock.
			if inSchemaBlock {
				continue
			}

			if inBlock {
				break // Closing delimiter.
			}

			inBlock = true

			continue
		}

		// A @schema delimiter ends an unclosed root block (root content
		// cannot extend into schema blocks) and otherwise toggles
		// schema-block state so root markers inside a schema block are
		// ignored as junk.
		if after, ok := strings.CutPrefix(trimmed, "@schema"); ok && isDelimiterSuffix(after) {
			if inBlock {
				break
			}

			inSchemaBlock = !inSchemaBlock

			continue
		}

		if inBlock {
			content = append(content, stripped)
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
		content := stripCommentHash(line)
		stripped := strings.TrimSpace(content)

		if after, ok := strings.CutPrefix(stripped, "@schema"); ok {
			rest := strings.TrimSpace(after)

			// Check for @schema.root delimiter (no trailing content).
			// Inside an open @schema block the marker is junk content,
			// not a delimiter, mirroring extractSchemaBlock.
			if rootAfter, isRoot := strings.CutPrefix(rest, ".root"); isRoot {
				if !inSchemaBlock && strings.TrimSpace(rootAfter) == "" {
					inRootBlock = !inRootBlock
				}

				continue
			}

			// @schema delimiter. It also ends an unclosed @schema.root
			// block.
			if isDelimiterSuffix(after) {
				inRootBlock = false
				inSchemaBlock = !inSchemaBlock

				continue
			}

			// @schema with inline content (losisin format) -- skip.
			continue
		}

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

// stripCommentHash removes leading whitespace, up to two leading '#'
// characters, and a single following space. Keeping only one space means
// deeper indentation after "# " survives for nested YAML block content.
func stripCommentHash(line string) string {
	line = strings.TrimSpace(line)

	for range 2 {
		line = strings.TrimPrefix(line, "#")
	}

	return strings.TrimPrefix(line, " ")
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

		switch len(types) {
		case 0:
			// No usable type strings remain (e.g. type: [], type: [1, 2]).
			// Leave Type and Types unset so structural inference and the
			// fail-open default apply: setting a non-nil empty Types here
			// would emit an invalid "type": [] and, once value inference
			// fills Type, collide as "both Type and Types are set", which
			// breaks the whole document's final marshal.
		case 1:
			schema.Type = types[0]
			schema.Types = nil

		default:
			schema.Type = ""
			schema.Types = types
		}
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
			if schema.DependencySchemas == nil {
				schema.DependencySchemas = make(map[string]*jsonschema.Schema)
			}

			schema.DependencySchemas[key] = magicschema.ToSubSchema(dv)
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
