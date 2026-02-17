package dadav

import (
	"log/slog"
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

// Name returns the annotator name.
func (a *Annotator) Name() string {
	return "helm-schema"
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
				schema.Examples = arr
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
			schema.Enum = arr
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
			schema.Examples = arr
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
		schema.AdditionalItems = toAdditionalItems(val)
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
		stripped := strings.TrimSpace(line)

		// Strip up to two leading '#' characters.
		for range 2 {
			stripped = strings.TrimPrefix(stripped, "#")
		}

		stripped = strings.TrimSpace(stripped)

		// Check for @schema.root delimiter (toggle root block state).
		if after, ok := strings.CutPrefix(stripped, "@schema.root"); ok {
			if strings.TrimSpace(after) == "" {
				inRootBlock = !inRootBlock
			}

			continue
		}

		// Skip lines inside @schema.root blocks.
		if inRootBlock {
			continue
		}

		if after, ok := strings.CutPrefix(stripped, "@schema"); ok {
			rest := strings.TrimSpace(after)

			if rest == "" {
				// Toggle delimiter -- supports multiple blocks.
				inBlock = !inBlock

				continue
			}

			// @schema with content on same line -- not this annotator
			// (that's helm-values-schema inline format).
			continue
		}

		if inBlock {
			// Strip up to two leading '#' characters plus optional space.
			contentLine := strings.TrimSpace(line)

			for range 2 {
				contentLine = strings.TrimPrefix(contentLine, "#")
			}

			contentLine = strings.TrimPrefix(contentLine, " ")

			content = append(content, contentLine)
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
		inBlock bool
		content []string
	)

	for _, line := range lines {
		stripped := strings.TrimSpace(line)

		for range 2 {
			stripped = strings.TrimPrefix(stripped, "#")
		}

		stripped = strings.TrimSpace(stripped)

		if after, ok := strings.CutPrefix(stripped, "@schema.root"); ok {
			rest := strings.TrimSpace(after)
			if rest != "" {
				// @schema.root with trailing content -- skip.
				continue
			}

			if inBlock {
				break // Closing delimiter.
			}

			inBlock = true

			continue
		}

		if inBlock {
			// Strip up to two leading '#' characters plus optional space.
			contentLine := strings.TrimSpace(line)

			for range 2 {
				contentLine = strings.TrimPrefix(contentLine, "#")
			}

			contentLine = strings.TrimPrefix(contentLine, " ")

			content = append(content, contentLine)
		}
	}

	if len(content) == 0 {
		return ""
	}

	return strings.Join(content, "\n")
}

// extractNonAnnotationDescription extracts description text from comments
// that are not part of @schema or @schema.root blocks.
func extractNonAnnotationDescription(comment string) string {
	lines := strings.Split(comment, "\n")

	var (
		descLines     []string
		inSchemaBlock bool
		inRootBlock   bool
	)

	for _, line := range lines {
		stripped := strings.TrimSpace(line)

		for range 2 {
			stripped = strings.TrimPrefix(stripped, "#")
		}

		stripped = strings.TrimSpace(stripped)

		if after, ok := strings.CutPrefix(stripped, "@schema"); ok {
			rest := strings.TrimSpace(after)

			// Check for @schema.root delimiter (no trailing content).
			if rootAfter, isRoot := strings.CutPrefix(rest, ".root"); isRoot {
				if strings.TrimSpace(rootAfter) == "" {
					inRootBlock = !inRootBlock
				}

				continue
			}

			// Bare @schema delimiter (no content after it).
			if rest == "" {
				inSchemaBlock = !inSchemaBlock

				continue
			}

			// @schema with inline content (losisin format) -- skip.
			continue
		}

		if inSchemaBlock || inRootBlock {
			continue
		}

		// Regular comment line.
		cleaned := cleanCommentLine(line)
		if magicschema.IsAnnotationComment(cleaned) {
			continue
		}

		descLines = append(descLines, cleaned)
	}

	// Use only lines after the last blank line.
	lastBlank := -1

	for i, line := range descLines {
		if strings.TrimSpace(line) == "" {
			lastBlank = i
		}
	}

	start := 0
	if lastBlank >= 0 {
		start = lastBlank + 1
	}

	// Filter blank lines from the final result.
	var nonBlank []string

	for _, l := range descLines[start:] {
		if strings.TrimSpace(l) != "" {
			nonBlank = append(nonBlank, l)
		}
	}

	if len(nonBlank) == 0 {
		return ""
	}

	return strings.Join(nonBlank, " ")
}

// cleanCommentLine strips up to two leading '#' characters plus optional space
// from a single comment line.
func cleanCommentLine(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#") {
		return line
	}

	// Strip up to two leading '#' characters.
	for range 2 {
		line = strings.TrimPrefix(line, "#")
	}

	line = strings.TrimPrefix(line, " ")

	// Strip helm-docs "-- " prefix.
	line = strings.TrimPrefix(line, "-- ")

	return strings.TrimSpace(line)
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

		if len(types) == 1 {
			schema.Type = types[0]
		} else {
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

// toAdditionalItems converts a value to an additionalItems schema.
// Upstream supports both boolean and schema values for additionalItems.
func toAdditionalItems(val any) *jsonschema.Schema {
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

// toAdditionalProperties converts a value to an additionalProperties schema.
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

// toFloat64Ptr converts a numeric value to *float64.
func toFloat64Ptr(val any) *float64 {
	switch v := val.(type) {
	case float64:
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

// toIntPtr converts a numeric value to *int.
func toIntPtr(val any) *int {
	switch v := val.(type) {
	case int:
		return &v
	case int64:
		i := int(v) //nolint:gosec // JSON Schema constraints are small values

		return &i

	case uint64:
		i := int(v) //nolint:gosec // JSON Schema constraints are small values

		return &i

	case float64:
		i := int(v)

		return &i
	}

	return nil
}
