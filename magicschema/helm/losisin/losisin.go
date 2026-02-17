package losisin

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/magicschema"
)

const yamlNull = "null"

// Annotator parses single-line # @schema annotations from the
// losisin/helm-values-schema-json project.
type Annotator struct{}

// New creates a new helm-values-schema inline annotator.
func New() *Annotator {
	return &Annotator{}
}

// Name returns the annotator name.
func (a *Annotator) Name() string {
	return "helm-values-schema"
}

// ForContent returns a new Annotator ready to process the given file.
func (a *Annotator) ForContent(_ []byte) (magicschema.Annotator, error) {
	return New(), nil
}

// Annotate extracts schema annotations from inline # @schema comments.
func (a *Annotator) Annotate(node ast.Node, _ string) *magicschema.AnnotationResult {
	mvn, ok := node.(*ast.MappingValueNode)
	if !ok {
		return nil
	}

	// Collect all comment lines from head, inline on value, inline on key, and foot.
	var commentLines []string

	if comment := mvn.GetComment(); comment != nil {
		commentLines = append(commentLines, lastCommentGroup(comment.String())...)
	}

	if mvn.Value != nil {
		if comment := mvn.Value.GetComment(); comment != nil {
			commentLines = append(commentLines, strings.Split(comment.String(), "\n")...)
		}
	}

	if mvn.Key != nil {
		if keyNode, ok := mvn.Key.(ast.Node); ok {
			if comment := keyNode.GetComment(); comment != nil {
				commentLines = append(commentLines, strings.Split(comment.String(), "\n")...)
			}
		}
	}

	if mvn.FootComment != nil {
		commentLines = append(commentLines, strings.Split(mvn.FootComment.String(), "\n")...)
	}

	// Find inline @schema annotations.
	var schemaLines []string

	for _, line := range commentLines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}

		trimmed = strings.TrimPrefix(trimmed, "#")
		trimmed = strings.TrimSpace(trimmed)

		if after, ok := strings.CutPrefix(trimmed, "@schema"); ok {
			// Require a space or end-of-string after "@schema" to avoid
			// matching "@schemafoo" as an annotation (matches upstream behavior).
			if after != "" && after[0] != ' ' && after[0] != '\t' {
				continue
			}

			rest := strings.TrimSpace(after)

			if rest != "" {
				// This is an inline annotation (not block delimiter).
				schemaLines = append(schemaLines, rest)
			}
		}
	}

	if len(schemaLines) == 0 {
		return nil
	}

	schema := &jsonschema.Schema{}
	result := &magicschema.AnnotationResult{Schema: schema}

	for _, line := range schemaLines {
		a.parseLine(schema, result, line)
	}

	return result
}

// parseLine parses a semicolon-separated key:value line into schema fields.
func (a *Annotator) parseLine(schema *jsonschema.Schema, result *magicschema.AnnotationResult, line string) {
	pairs := splitSemicolons(line)

	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		key, val, hasVal := strings.Cut(pair, ":")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		a.applyPair(schema, result, key, val, hasVal)
	}
}

// applyPair applies a single key:value pair to the schema.
//
//nolint:cyclop,gocyclo,goconst // dispatching all supported keys requires many cases and repeated field names
func (a *Annotator) applyPair(
	schema *jsonschema.Schema,
	result *magicschema.AnnotationResult,
	key, val string,
	hasVal bool,
) {
	switch key {
	case "type":
		a.applyType(schema, val)
	case "title":
		schema.Title = val
	case "description":
		schema.Description = val
	case "default":
		schema.Default = magicschema.ParseYAMLValue(val)
	case "enum":
		schema.Enum = parseAnyList(val)
	case "const":
		v, ok := parseYAMLValue(val)
		if ok {
			schema.Const = magicschema.ConstValue(v)
		}

	case "pattern":
		schema.Pattern = val
	case "multipleOf":
		schema.MultipleOf = parsePositiveFloat64Ptr(val)
	case "minimum":
		schema.Minimum = parseFloat64Ptr(val)
	case "maximum":
		schema.Maximum = parseFloat64Ptr(val)
	case "minLength":
		schema.MinLength = parseIntPtr(val)
	case "maxLength":
		schema.MaxLength = parseIntPtr(val)
	case "minItems":
		schema.MinItems = parseIntPtr(val)
	case "maxItems":
		schema.MaxItems = parseIntPtr(val)
	case "minProperties":
		schema.MinProperties = parseIntPtr(val)
	case "maxProperties":
		schema.MaxProperties = parseIntPtr(val)
	case "uniqueItems":
		schema.UniqueItems = parseBoolDefault(val)
	case "required":
		b := parseBoolDefault(val)
		result.HasRequired = &b

	case "readOnly":
		schema.ReadOnly = parseBoolDefault(val)
	case "examples":
		schema.Examples = parseAnyList(val)
	case "additionalProperties":
		switch {
		case !hasVal || val == "":
			schema.AdditionalProperties = magicschema.TrueSchema()
		case val == "true":
			schema.AdditionalProperties = magicschema.TrueSchema()
		case val == "false":
			schema.AdditionalProperties = magicschema.FalseSchema()
		default:
			// Try parsing as a schema object.
			if s := parseYAMLSchema(val); s != nil {
				schema.AdditionalProperties = s
			} else {
				schema.AdditionalProperties = magicschema.TrueSchema()
			}
		}

	case "patternProperties":
		schema.PatternProperties = parseYAMLSchemaMap(val)
	case "allOf":
		schema.AllOf = parseYAMLSchemaArray(val)
	case "anyOf":
		schema.AnyOf = parseYAMLSchemaArray(val)
	case "oneOf":
		schema.OneOf = parseYAMLSchemaArray(val)
	case "not":
		s := parseYAMLSchema(val)
		if s != nil {
			schema.Not = s
		}

	case "$id":
		schema.ID = val
	case "$ref":
		schema.Ref = val
	case "item":
		if schema.Items == nil {
			schema.Items = &jsonschema.Schema{}
		}

		types := parseStringList(val)

		switch len(types) {
		case 0:
			// Empty or unparseable value; skip.
		case 1:
			schema.Items.Type = types[0]
		default:
			schema.Items.Types = types
		}

	case "itemProperties":
		if schema.Items == nil {
			schema.Items = &jsonschema.Schema{}
		}

		schema.Items.Properties = parseYAMLSchemaMap(val)

	case "itemEnum":
		if schema.Items == nil {
			schema.Items = &jsonschema.Schema{}
		}

		schema.Items.Enum = parseAnyList(val)

	case "itemRef":
		if schema.Items == nil {
			schema.Items = &jsonschema.Schema{}
		}

		schema.Items.Ref = val

	case "hidden":
		result.Skip = parseBoolDefault(val)
	case "skipProperties":
		result.SkipProperties = parseBoolDefault(val)
	case "mergeProperties":
		result.MergeProperties = parseBoolDefault(val)
	case "unevaluatedProperties":
		switch val {
		case "false":
			schema.UnevaluatedProperties = magicschema.FalseSchema()
		case "", "true":
			schema.UnevaluatedProperties = magicschema.TrueSchema()
		default:
			// Try parsing as a schema object.
			if s := parseYAMLSchema(val); s != nil {
				schema.UnevaluatedProperties = s
			} else {
				schema.UnevaluatedProperties = magicschema.TrueSchema()
			}
		}

	default:
		slog.Warn("unknown helm-values-schema key", slog.String("key", key))
	}
}

// applyType parses a type string which may be a single type, a bracket-delimited
// array like [string, integer], or a comma-separated list like "string, null".
// The upstream tool uses processList with stringsOnly=true, which first tries YAML
// parse for bracket-prefixed values, then falls back to comma-splitting.
func (a *Annotator) applyType(schema *jsonschema.Schema, val string) {
	types := parseStringList(val)

	switch len(types) {
	case 0:
		// Empty or unparseable value; skip.
	case 1:
		schema.Type = types[0]
	default:
		schema.Types = types
	}
}

// splitSemicolons splits a line by semicolons, respecting brackets.
func splitSemicolons(line string) []string {
	var (
		parts   []string
		current strings.Builder
		depth   int
	)

	for _, ch := range line {
		switch ch {
		case '[', '{':
			depth++

			current.WriteRune(ch)

		case ']', '}':
			depth--

			current.WriteRune(ch)

		case ';':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
			} else {
				current.WriteRune(ch)
			}

		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// parseStringList parses a list value where all elements are coerced to strings.
// It first tries YAML array parsing for bracket-prefixed values, then falls back
// to comma-splitting. This matches the upstream processList(comment, stringsOnly=true)
// behavior.
func parseStringList(val string) []string {
	val = strings.TrimSpace(val)

	// Try YAML parse first for bracket-prefixed values.
	if strings.HasPrefix(val, "[") {
		var list []any

		err := yaml.Unmarshal([]byte(val), &list)
		if err == nil {
			result := make([]string, 0, len(list))

			for _, v := range list {
				switch v := v.(type) {
				case string:
					result = append(result, v)
				case nil:
					result = append(result, yamlNull)
				default:
					result = append(result, fmt.Sprint(v))
				}
			}

			return result
		}
	}

	// Fall back to comma-splitting. Strip brackets if present (handles
	// malformed YAML arrays that failed to parse above).
	inner := val
	if after, ok := strings.CutPrefix(val, "["); ok {
		inner = strings.TrimSuffix(after, "]")
	}

	var result []string

	for item := range strings.SplitSeq(inner, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}

	return result
}

// parseAnyList parses a list value preserving native types (null stays nil,
// numbers stay numeric, booleans stay booleans). It first tries YAML array
// parsing for bracket-prefixed values, then falls back to comma-splitting.
// This matches the upstream processList(comment, stringsOnly=false) behavior.
func parseAnyList(val string) []any {
	val = strings.TrimSpace(val)

	// Try YAML parse first for bracket-prefixed values.
	if strings.HasPrefix(val, "[") {
		var list []any

		err := yaml.Unmarshal([]byte(val), &list)
		if err == nil {
			return list
		}
	}

	// Fall back to comma-splitting. Strip brackets if present.
	inner := val
	if after, ok := strings.CutPrefix(val, "["); ok {
		inner = strings.TrimSuffix(after, "]")
	}

	var list []any

	for item := range strings.SplitSeq(inner, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		if item == yamlNull {
			list = append(list, nil)
			continue
		}

		// Try to unquote quoted strings.
		if strings.HasPrefix(item, "\"") {
			unquoted, err := strconv.Unquote(item)
			if err == nil {
				list = append(list, unquoted)
				continue
			}
		}

		// Trim surrounding quotes and use as-is.
		list = append(list, strings.Trim(item, "\""))
	}

	return list
}

// parseYAMLAny parses a YAML string into any Go value.
func parseYAMLAny(val string) any {
	var v any

	err := yaml.Unmarshal([]byte(val), &v)
	if err != nil {
		return nil
	}

	return v
}

// parseYAMLValue parses a YAML string into any Go value, distinguishing
// between parse errors and explicit null values.
func parseYAMLValue(val string) (any, bool) {
	var v any

	err := yaml.Unmarshal([]byte(val), &v)
	if err != nil {
		return nil, false
	}

	return v, true
}

// parseYAMLSchema parses a YAML object string into *jsonschema.Schema.
func parseYAMLSchema(val string) *jsonschema.Schema {
	v := parseYAMLAny(val)
	if v == nil {
		return nil
	}

	return magicschema.ToSubSchema(v)
}

// parseYAMLSchemaArray parses a YAML array string into []*jsonschema.Schema.
func parseYAMLSchemaArray(val string) []*jsonschema.Schema {
	v := parseYAMLAny(val)
	if v == nil {
		return nil
	}

	return magicschema.ToSubSchemaArray(v)
}

// parseYAMLSchemaMap parses a YAML object string into map[string]*jsonschema.Schema.
func parseYAMLSchemaMap(val string) map[string]*jsonschema.Schema {
	v := parseYAMLAny(val)
	if v == nil {
		return nil
	}

	return magicschema.ToSubSchemaMap(v)
}

// lastCommentGroup extracts lines from the last comment group in a
// multi-line comment string. Comment groups are separated by blank lines.
// This matches upstream behavior where only the final group before a key
// is considered for annotations.
func lastCommentGroup(s string) []string {
	lines := strings.Split(s, "\n")

	lastBlank := -1

	for i, line := range lines {
		stripped := strings.TrimSpace(line)
		stripped = strings.TrimLeft(stripped, "#")
		stripped = strings.TrimSpace(stripped)

		if stripped == "" {
			lastBlank = i
		}
	}

	if lastBlank >= 0 && lastBlank < len(lines)-1 {
		return lines[lastBlank+1:]
	}

	return lines
}

// parseFloat64Ptr parses a string into *float64. Returns nil for "null"
// (matching upstream behavior where null clears the constraint) and for
// unparseable values.
func parseFloat64Ptr(val string) *float64 {
	if strings.TrimSpace(val) == yamlNull {
		return nil
	}

	var v float64

	err := yaml.Unmarshal([]byte(val), &v)
	if err != nil {
		return nil
	}

	return &v
}

// parsePositiveFloat64Ptr parses a string into *float64, returning nil if
// the value is not positive or is "null". This matches upstream multipleOf
// validation where the value must be strictly greater than zero.
func parsePositiveFloat64Ptr(val string) *float64 {
	v := parseFloat64Ptr(val)
	if v == nil || *v <= 0 {
		return nil
	}

	return v
}

// parseIntPtr parses a string into *int, returning nil for negative values
// and "null". Upstream uses uint64 for length/count constraints, rejecting
// negatives and accepting "null" to clear the constraint.
func parseIntPtr(val string) *int {
	if strings.TrimSpace(val) == yamlNull {
		return nil
	}

	var v int

	err := yaml.Unmarshal([]byte(val), &v)
	if err != nil {
		return nil
	}

	if v < 0 {
		return nil
	}

	return &v
}

// parseBoolDefault parses a string as a boolean. An empty value (bare keyword
// with no colon) returns true, matching upstream behavior where "# @schema required"
// is equivalent to "# @schema required:true". Only "true" and "false" (case-
// insensitive per YAML spec) are recognized; unrecognized values are treated as
// false (fail-open: don't add restrictions for garbage input).
func parseBoolDefault(val string) bool {
	val = strings.TrimSpace(val)
	if val == "" {
		return true
	}

	switch strings.ToLower(val) {
	case "true":
		return true
	case "false":
		return false
	default:
		slog.Warn("invalid boolean for helm-values-schema annotation",
			slog.String("value", val))

		return false
	}
}
