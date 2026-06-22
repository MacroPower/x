package losisin

import (
	"log/slog"
	"math"
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

// Name is the canonical annotator name, used as the registry key and in
// the --annotators flag.
const Name = "helm-values-schema"

// Name returns the annotator name.
func (a *Annotator) Name() string {
	return Name
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
		// Only the final comment group before a key is considered for
		// annotations, matching upstream behavior.
		commentLines = append(commentLines,
			magicschema.LastCommentGroup(strings.Split(comment.String(), "\n"))...)
	}

	// Key-line comment before value-line comment so that, under last-wins
	// resolution, the value-line annotation wins -- the order upstream
	// helm-values-schema collects them (keyNode.LineComment, then
	// valNode.LineComment).
	if mvn.Key != nil {
		if keyNode, ok := mvn.Key.(ast.Node); ok {
			if comment := keyNode.GetComment(); comment != nil {
				commentLines = append(commentLines, strings.Split(comment.String(), "\n")...)
			}
		}
	}

	if mvn.Value != nil {
		if comment := mvn.Value.GetComment(); comment != nil {
			commentLines = append(commentLines, strings.Split(comment.String(), "\n")...)
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
		// An empty value carries no default (explicit null is written as
		// "default:null"); setting it would emit a spurious null default.
		if val != "" {
			schema.Default = magicschema.ParseYAMLValue(val)
		}

	case "enum":
		schema.Enum = magicschema.FilterJSONSafe(parseAnyList(val))
	case "const":
		// An empty value carries no const (explicit null is "const:null");
		// emitting const:null would reject the real value (fail-closed),
		// mirroring the default: guard above.
		if val != "" {
			v, ok := parseYAMLValue(val)
			if ok {
				schema.Const = magicschema.ConstValue(v)
			}
		}

	case "pattern":
		schema.Pattern = unquoteScalar(val)
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
		schema.Examples = magicschema.FilterJSONSafe(parseAnyList(val))
	case "additionalProperties":
		schema.AdditionalProperties = parseBoolOrSchema(val, hasVal)

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
		schema.ID = unquoteScalar(val)
	case "$ref":
		schema.Ref = unquoteScalar(val)
	case "item":
		// Guard ensureItems on a non-empty value: a bare or empty-valued
		// item* key would otherwise create an empty Items schema, which
		// marshals to "true" and suppresses the generator's element
		// inference, leaving the array less described than the un-annotated
		// form.
		if types := parseStringList(val); len(types) > 0 {
			magicschema.SetSchemaType(ensureItems(schema), types)
		}

	case "itemProperties":
		if props := parseYAMLSchemaMap(val); len(props) > 0 {
			ensureItems(schema).Properties = props
		}

	case "itemEnum":
		if enum := magicschema.FilterJSONSafe(parseAnyList(val)); len(enum) > 0 {
			ensureItems(schema).Enum = enum
		}

	case "itemRef":
		if val != "" {
			ensureItems(schema).Ref = val
		}

	case "hidden":
		result.Skip = parseBoolDefault(val)
	case "skipProperties":
		result.SkipProperties = parseBoolDefault(val)
	case "mergeProperties":
		result.MergeProperties = parseBoolDefault(val)
	case "unevaluatedProperties":
		schema.UnevaluatedProperties = parseBoolOrSchema(val, hasVal)

	default:
		slog.Warn("unknown helm-values-schema key", slog.String("key", key))
	}
}

// applyType parses a type string which may be a single type, a bracket-delimited
// array like [string, integer], or a comma-separated list like "string, null".
// The upstream tool uses processList with stringsOnly=true, which first tries YAML
// parse for bracket-prefixed values, then falls back to comma-splitting.
func (a *Annotator) applyType(schema *jsonschema.Schema, val string) {
	magicschema.SetSchemaType(schema, parseStringList(val))
}

// ensureItems lazily initializes and returns the schema's Items, so the item*
// annotation keys (item, itemProperties, itemEnum, itemRef) populate one shared
// items schema without each repeating the nil guard.
func ensureItems(s *jsonschema.Schema) *jsonschema.Schema {
	if s.Items == nil {
		s.Items = &jsonschema.Schema{}
	}

	return s.Items
}

// parseBoolOrSchema resolves a bool-or-schema annotation value, shared by the
// additionalProperties and unevaluatedProperties keys: a bare keyword, empty
// value, or "true" permits everything; "false" forbids everything; anything
// else parses as a schema object, falling back to permit-everything (fail-open)
// when it does not parse.
func parseBoolOrSchema(val string, hasVal bool) *jsonschema.Schema {
	switch {
	case !hasVal || val == "" || val == "true":
		return magicschema.TrueSchema()
	case val == "false":
		return magicschema.FalseSchema()
	default:
		if s := parseYAMLSchema(val); s != nil {
			return s
		}

		return magicschema.TrueSchema()
	}
}

// splitSemicolons splits a line by semicolons, respecting nested brackets and
// quoted runs. Bracket nesting is tracked with a type-aware stack, so a stray
// closer of one kind (a "}" inside a "[...]" value) does not cancel an opener
// of the other kind and expose an inner semicolon as a delimiter. A quoted
// value -- single or double -- likewise keeps a ";" (or a bracket) it contains
// literal, so an annotation such as default:'a;b' or default:"a;b" survives
// intact; inside a double-quoted run a backslash escapes the next rune, so an
// escaped quote (default:"a\";b") does not end the run early. A quote only
// opens a run at bracket depth zero -- inside [...] or {...} the bracket stack
// already keeps the content literal, so a quote in a regex char class like
// [",;] is not mistaken for a string delimiter. When openers or a quote are
// left unbalanced the split is unreliable, so the line is split on every
// semicolon instead -- a malformed value then only corrupts its own pair
// rather than swallowing every pair after it.
func splitSemicolons(line string) []string {
	var (
		parts   []string
		current strings.Builder
		stack   []rune
		quote   rune // 0 outside a quoted run, else the opening quote rune
		escaped bool
	)

	for _, ch := range line {
		// Inside a quoted run every character is literal: a ";" or a bracket
		// there is part of the value, not a delimiter or nesting token. In a
		// double-quoted run a backslash escapes the next rune, so an escaped
		// quote (default:"a\";b") does not close the run and expose the inner
		// semicolon; single-quoted YAML has no backslash escape.
		if quote != 0 {
			current.WriteRune(ch)

			switch {
			case escaped:
				escaped = false
			case quote == '"' && ch == '\\':
				escaped = true
			case ch == quote:
				quote = 0
			}

			continue
		}

		switch ch {
		case '"', '\'':
			// Only a top-level quote opens a quoted run. Inside [...] or {...}
			// the bracket stack already keeps the content literal -- a regex
			// char class like [",;], say -- so opening a run there would
			// swallow the matching closer and force the naive whole-line split.
			if len(stack) == 0 {
				quote = ch
			}

			current.WriteRune(ch)

		case '[', '{':
			stack = append(stack, ch)

			current.WriteRune(ch)

		case ']', '}':
			// Pop only a matching opener; a stray closer of the other kind
			// is a literal and leaves the nesting depth untouched.
			if n := len(stack); n > 0 && stack[n-1] == matchingOpen(ch) {
				stack = stack[:n-1]
			}

			current.WriteRune(ch)

		case ';':
			if len(stack) == 0 {
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

	if len(stack) != 0 || quote != 0 {
		return strings.Split(line, ";")
	}

	return parts
}

// matchingOpen returns the opening bracket that pairs with a closing bracket.
func matchingOpen(closer rune) rune {
	if closer == '}' {
		return '{'
	}

	return '['
}

// splitListValue implements the list scaffold shared by parseStringList and
// parseAnyList, matching the upstream processList: YAML array parsing for
// bracket-prefixed values, with a comma-splitting fallback that strips
// brackets (handling malformed YAML arrays) and surrounding space from each
// non-empty item. When the boolean is true the YAML elements are returned;
// otherwise the fallback items are.
func splitListValue(val string) ([]any, []string, bool) {
	val = strings.TrimSpace(val)

	// Try YAML parse first for bracket-prefixed values.
	if strings.HasPrefix(val, "[") {
		var list []any

		err := yaml.Unmarshal([]byte(val), &list)
		if err == nil {
			return list, nil, true
		}
	}

	inner := val
	if after, found := strings.CutPrefix(val, "["); found {
		inner = strings.TrimSuffix(after, "]")
	}

	var items []string

	for item := range strings.SplitSeq(inner, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}

	return nil, items, false
}

// parseStringList parses a list value where all elements are coerced to
// strings. This matches the upstream processList(comment, stringsOnly=true)
// behavior.
func parseStringList(val string) []string {
	parsed, items, ok := splitListValue(val)
	if !ok {
		// The comma-split fallback returns raw tokens. Coerce each through
		// YAML the same way the bracket path does so the two forms agree:
		// without this, "type:string, 1" kept the invalid token "1" while
		// "type:[string, 1]" dropped it.
		parsed = make([]any, 0, len(items))

		for _, item := range items {
			var v any

			err := yaml.Unmarshal([]byte(item), &v)
			if err != nil {
				v = item
			}

			parsed = append(parsed, v)
		}
	}

	result := make([]string, 0, len(parsed))

	// A member that is neither a string nor null cannot be a JSON Schema type
	// token (e.g. type:[string, 1]). Narrowing to just the representable
	// members would keep type:string and reject an integer the value may take
	// (fail closed), and would also make the same malformed annotation differ
	// from dadav's applyType, which abandons the whole type. Drop the entire
	// list instead so SetSchemaType leaves the type unset and value inference
	// applies (fail open).
	for _, v := range parsed {
		switch v := v.(type) {
		case string:
			result = append(result, v)
		case nil:
			result = append(result, yamlNull)
		default:
			return nil
		}
	}

	return result
}

// parseAnyList parses a list value preserving native types (null stays nil,
// numbers stay numeric, booleans stay booleans). This matches the upstream
// processList(comment, stringsOnly=false) behavior.
func parseAnyList(val string) []any {
	parsed, items, ok := splitListValue(val)
	if ok {
		return parsed
	}

	var list []any

	for _, item := range items {
		if item == yamlNull {
			list = append(list, nil)

			continue
		}

		// Strip the surrounding quotes from a fully quoted token -- single or
		// double -- so a quote needed only to protect a comma or space does not
		// leak into the value. The double-quote-only unquoting this replaced
		// left single-quoted items with their quotes ('a' stayed "'a'"), so an
		// enum or itemEnum member never matched the real value (fail closed).
		// An unquoted token stays a string, matching upstream processList's
		// string-typed comma-split fallback (numbers and booleans keep native
		// types only on the bracketed YAML path).
		list = append(list, unquoteScalar(item))
	}

	return list
}

// unquoteScalar strips matching surrounding quotes from an inline scalar value.
// A pattern, $id, or $ref containing a ";" must be quoted to survive
// splitSemicolons, but the raw value then keeps the quote characters -- a
// pattern like "^a$" would carry the literal quotes into the regex and reject
// the value it should match. Parsing a fully quoted scalar through YAML yields
// the bare text, matching how default and const are already unquoted. A bare
// value is returned unchanged so it is never re-coerced.
//
// When the YAML parse fails the surrounding quotes are still stripped manually
// rather than left on the value: a double-quoted regex such as "^\d+$" is not a
// valid YAML double-quoted scalar (\d is not a recognized escape), and keeping
// the quotes would build a regex that requires literal leading and trailing
// quote characters (fail closed). The first and last bytes were already
// confirmed equal quote runes, so the slice is safe.
func unquoteScalar(val string) string {
	if len(val) < 2 {
		return val
	}

	first := val[0]
	if (first != '"' && first != '\'') || val[len(val)-1] != first {
		return val
	}

	var s string

	err := yaml.Unmarshal([]byte(val), &s)
	if err != nil {
		return val[1 : len(val)-1]
	}

	return s
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

// parseYAMLSchema parses a YAML object string into *jsonschema.Schema. A parse
// failure or explicit null yields a nil value, which ToSubSchema maps to nil.
func parseYAMLSchema(val string) *jsonschema.Schema {
	v, _ := parseYAMLValue(val)

	return magicschema.ToSubSchema(v)
}

// parseYAMLSchemaArray parses a YAML array string into []*jsonschema.Schema.
func parseYAMLSchemaArray(val string) []*jsonschema.Schema {
	v, _ := parseYAMLValue(val)

	return magicschema.ToSubSchemaArray(v)
}

// parseYAMLSchemaMap parses a YAML object string into map[string]*jsonschema.Schema.
func parseYAMLSchemaMap(val string) map[string]*jsonschema.Schema {
	v, _ := parseYAMLValue(val)

	return magicschema.ToSubSchemaMap(v)
}

// parseFloat64Ptr parses a string into *float64. Returns nil for an empty or
// "null" value (both clear the constraint, matching upstream and keeping the
// bare-keyword form fail-open), for unparseable values, and for non-finite
// results (.inf/.nan), which YAML accepts but JSON cannot marshal -- letting
// one through would fail the whole schema's final marshal.
func parseFloat64Ptr(val string) *float64 {
	s := strings.TrimSpace(val)
	if s == "" || s == yamlNull {
		return nil
	}

	var v float64

	err := yaml.Unmarshal([]byte(s), &v)
	if err != nil {
		return nil
	}

	if math.IsInf(v, 0) || math.IsNaN(v) {
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

// parseIntPtr parses a string into *int, returning nil for an empty or "null"
// value and for negative values. An empty value clears the constraint (a bare
// "maxItems:" must not become the fail-closed maxItems:0). Floats are truncated
// rather than rejected, an intentional divergence from upstream's ParseUint
// (see the upstream-alignment tests): the generator accepts more input rather
// than dropping a usable-but-imprecise constraint.
func parseIntPtr(val string) *int {
	s := strings.TrimSpace(val)
	if s == "" || s == yamlNull {
		return nil
	}

	var v int

	err := yaml.Unmarshal([]byte(s), &v)
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
