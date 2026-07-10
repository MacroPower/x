package losisin

import (
	"log/slog"
	"math"
	"slices"
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

	// Collect the comment parts from head, key line, value line, and foot.
	// The shared collector compensates the goccy attribution quirks (merged
	// head groups narrowed to the documenting run, a sequence's stowed
	// first-element head comment excluded from the value-line part).
	nc := magicschema.CollectNodeComments(mvn)

	var commentLines []string

	// The head run may span several upstream comment groups; upstream keeps
	// only the last, splitting on the blank line. A "#"-only line inside the
	// run is a paragraph separator, not a group boundary
	// ([magicschema.LastCommentGroup] trims only the group's blank edges), so
	// an annotation separated from its prose by a bare "#" line still
	// applies, matching the upstream "\n\n"-only split.
	if nc.HeadRun != "" {
		commentLines = append(commentLines,
			magicschema.LastCommentGroup(strings.Split(nc.HeadRun, "\n"))...)
	}

	// Key-line comment before value-line comment so that, under last-wins
	// resolution, the value-line annotation wins -- the order upstream
	// helm-values-schema collects them (keyNode.LineComment, then
	// valNode.LineComment).
	if nc.KeyInline != "" {
		commentLines = append(commentLines, strings.Split(nc.KeyInline, "\n")...)
	}

	if nc.ValueInline != "" {
		commentLines = append(commentLines, strings.Split(nc.ValueInline, "\n")...)
	}

	if nc.Foot != "" {
		commentLines = append(commentLines, strings.Split(nc.Foot, "\n")...)
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
			if !magicschema.IsMarkerBoundary(after) {
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

	var nullable bool

	for _, line := range schemaLines {
		a.parseLine(schema, result, line, &nullable)
	}

	// The nullable key applies after every pair so it works on both sides of
	// type: within a line and across lines alike.
	if nullable {
		applyNullable(schema)
	}

	return result
}

// parseLine parses a semicolon-separated key:value line into schema fields.
// The nullable flag is collected rather than applied: it widens the type only
// after every pair has been seen.
func (a *Annotator) parseLine(
	schema *jsonschema.Schema,
	result *magicschema.AnnotationResult,
	line string,
	nullable *bool,
) {
	pairs := splitSemicolons(line)

	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		key, val, hasVal := strings.Cut(pair, ":")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		a.applyPair(schema, result, key, val, hasVal, nullable)
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
	nullable *bool,
) {
	switch key {
	case "type":
		magicschema.SetSchemaType(schema, parseStringList(val))
	case "title":
		schema.Title = unquoteScalar(val)
	case "description":
		schema.Description = unquoteScalar(val)
	case "default":
		// An empty value carries no default (explicit null is written as
		// "default:null"); setting it would emit a spurious null default. Only
		// a parsed value is assigned -- per the documented invalid-values
		// rule, an unparseable value on a repeated key must not clear a
		// previously set valid default with nil.
		if raw := magicschema.ParseYAMLValue(val); raw != nil {
			schema.Default = raw
		}

	case "enum":
		// Assign only a non-empty parse, mirroring the itemEnum guard: an
		// unparseable or empty value is silently skipped so a later typo on a
		// repeated key never clears a previously set valid constraint.
		if list := magicschema.FilterJSONSafe(parseAnyList(val)); len(list) > 0 {
			schema.Enum = list
		}

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
		applyNumericBound(&schema.MultipleOf, val, parsePositiveFloat64Ptr)
	case "minimum":
		applyNumericBound(&schema.Minimum, val, parseFloat64Ptr)
	case "maximum":
		applyNumericBound(&schema.Maximum, val, parseFloat64Ptr)
	case "minLength":
		applyNumericBound(&schema.MinLength, val, parseIntPtr)
	case "maxLength":
		applyNumericBound(&schema.MaxLength, val, parseIntPtr)
	case "minItems":
		applyNumericBound(&schema.MinItems, val, parseIntPtr)
	case "maxItems":
		applyNumericBound(&schema.MaxItems, val, parseIntPtr)
	case "minProperties":
		applyNumericBound(&schema.MinProperties, val, parseIntPtr)
	case "maxProperties":
		applyNumericBound(&schema.MaxProperties, val, parseIntPtr)
	case "uniqueItems":
		schema.UniqueItems = parseBoolDefault(val)
	case "required":
		// Only an explicit true/false (or the bare keyword, which upstream
		// reads as true) sets the tri-state signal. HasRequired's false is an
		// active opt-out -- it outranks a lower-priority annotator's explicit
		// required:true and cancels merge-key-inherited required -- unlike
		// every other boolean key's inert zero, so an unparseable value must
		// be skipped rather than converted into an opt-out no annotator
		// wrote.
		if b, ok := parseBool(val); ok {
			result.HasRequired = &b
		}

	case "readOnly":
		schema.ReadOnly = parseBoolDefault(val)
	case "deprecated":
		schema.Deprecated = parseBoolDefault(val)
	case "nullable":
		// Collected, not applied: the null type appends only after every
		// pair, so nullable works regardless of where it sits relative to
		// type:. A false value stays inert rather than un-nulling a type
		// union another pair set.
		if parseBoolDefault(val) {
			*nullable = true
		}

	case "examples":
		// Assign only a non-empty parse, the same guard as enum above.
		if list := magicschema.FilterJSONSafe(parseAnyList(val)); len(list) > 0 {
			schema.Examples = list
		}

	case "additionalProperties":
		schema.AdditionalProperties = parseBoolOrSchema(val, hasVal)

	case "patternProperties":
		// Assign only a parsed value: an unparseable one is silently skipped
		// (per the documented invalid-values rule), so a later typo on a
		// repeated key never clears a previously set valid constraint. The
		// not and item* keys below already guard the same way.
		if m := parseYAMLSchemaMap(val); m != nil {
			schema.PatternProperties = m
		}

	case "allOf":
		if arr := parseYAMLSchemaArray(val); arr != nil {
			schema.AllOf = arr
		}

	case "anyOf":
		if arr := parseYAMLSchemaArray(val); arr != nil {
			schema.AnyOf = arr
		}

	case "oneOf":
		if arr := parseYAMLSchemaArray(val); arr != nil {
			schema.OneOf = arr
		}

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

	case "itemRequired":
		// A required list for the items schema of object-typed elements.
		// Assign only a non-empty parse (fail open on garbage), mirroring the
		// sibling item* guards.
		if req := parseRequiredNames(val); len(req) > 0 {
			ensureItems(schema).Required = req
		}

	case "itemRef":
		// Unquote like the $ref sibling: a ref containing a ";" must be
		// quoted to survive splitSemicolons, and the quotes must not leak
		// into the JSON pointer.
		if ref := unquoteScalar(val); ref != "" {
			ensureItems(schema).Ref = ref
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

// applyNullable appends "null" to the annotated type, matching upstream's
// appendNullType post-processing. With no annotated type it leaves the
// null-only type, which the generator widens with the value's inferred type
// -- the same contract bitnami's [nullable] modifier relies on.
func applyNullable(s *jsonschema.Schema) {
	var types []string

	switch {
	case s.Types != nil:
		types = append(slices.Clone(s.Types), yamlNull)
	case s.Type != "":
		types = []string{s.Type, yamlNull}
	default:
		types = []string{yamlNull}
	}

	magicschema.SetSchemaType(s, types)
}

// parseRequiredNames parses a property-name list for itemRequired through the
// shared token policy ([magicschema.StringTokens]): string members are kept
// (a partial list still guides, fail open) and repeats drop, since required
// is a Draft 7 set whose elements must be unique. The list scaffold is the
// same YAML-coerced parse parseStringList uses, so the bracketed and
// comma-split forms agree on every member -- a null in either form drops
// rather than surviving the fallback as the literal name "null".
func parseRequiredNames(val string) []string {
	return magicschema.StringTokens(parseListItems(val))
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
// escaped quote (default:"a\";b") does not end the run early, and inside a
// single-quoted run a doubled quote is YAML's literal-quote escape
// (default:'a”s;b'), not the closing delimiter. A quote only
// opens a run at bracket depth zero -- inside [...] or {...} the bracket stack
// already keeps the content literal, so a quote in a regex char class like
// [",;] is not mistaken for a string delimiter -- and only at the start of a
// value or pair (after ':', ';', or whitespace): an apostrophe inside
// unquoted prose ("description:don't overuse") is literal, so it cannot pair
// with a later apostrophe and swallow the ';' between two pairs. When openers
// or a quote are left unbalanced the split is unreliable, so the line is
// split on every semicolon instead -- a malformed value then only corrupts
// its own pair rather than swallowing every pair after it.
func splitSemicolons(line string) []string {
	var (
		parts   []string
		current strings.Builder
		stack   []rune
		quote   rune // 0 outside a quoted run, else the opening quote rune
		escaped bool
		prev    rune // previous rune outside quoted runs, 0 at line start
	)

	runes := []rune(line)

	for i := 0; i < len(runes); i++ {
		ch := runes[i]

		// Inside a quoted run every character is literal: a ";" or a bracket
		// there is part of the value, not a delimiter or nesting token. In a
		// double-quoted run a backslash escapes the next rune, so an escaped
		// quote (default:"a\";b") does not close the run and expose the inner
		// semicolon; in a single-quoted run a doubled quote is YAML's escape
		// for a literal quote (default:'a''s;b'), not the closing delimiter --
		// the same escape rules quoteSpansValue honors when unquoting.
		if quote != 0 {
			current.WriteRune(ch)

			switch {
			case escaped:
				escaped = false
			case quote == '"' && ch == '\\':
				escaped = true
			case ch == quote:
				if quote == '\'' && i+1 < len(runes) && runes[i+1] == '\'' {
					current.WriteRune(runes[i+1])

					i++

					continue
				}

				quote = 0
			}

			continue
		}

		atValueStart := prev == 0 || prev == ':' || prev == ';' || prev == ' ' || prev == '\t'
		prev = ch

		switch ch {
		case '"', '\'':
			// Only a top-level quote at the start of a value opens a quoted
			// run. Inside [...] or {...} the bracket stack already keeps the
			// content literal -- a regex char class like [",;], say -- and a
			// mid-word quote is prose; opening a run in either place would
			// swallow a later matching rune and corrupt neighboring pairs.
			if len(stack) == 0 && atValueStart {
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

// parseListItems parses a list value into its decoded members: the YAML
// array parse for bracket-prefixed values (see splitListValue), with the
// comma-split fallback coercing each token through YAML so the two forms
// agree -- without the coercion, "type:string, 1" kept the invalid token "1"
// while "type:[string, 1]" dropped it.
func parseListItems(val string) []any {
	parsed, items, ok := splitListValue(val)
	if ok {
		return parsed
	}

	parsed = make([]any, 0, len(items))

	for _, item := range items {
		var v any

		err := yaml.Unmarshal([]byte(item), &v)
		if err != nil {
			v = item
		}

		parsed = append(parsed, v)
	}

	return parsed
}

// parseStringList parses a list value where all elements are coerced to
// strings. This matches the upstream processList(comment, stringsOnly=true)
// behavior. Token normalization is the policy shared with dadav's applyType
// (see [magicschema.TypeTokens]): strings kept, nulls become the "null"
// type, and any other member drops the whole list so the same malformed
// annotation cannot produce different schemas in the two formats.
func parseStringList(val string) []string {
	return magicschema.TypeTokens(parseListItems(val))
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
// quote characters (fail closed). The manual strip applies only when the
// leading quote's run spans the whole value; text that merely starts and ends
// with the same quote character (a description like 'foo' and 'bar', a regex
// alternation like "^a"|"b$") is returned verbatim, matching the upstream's
// verbatim assignment.
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
		if quoteSpansValue(val) {
			return val[1 : len(val)-1]
		}

		return val
	}

	return s
}

// quoteSpansValue reports whether a value whose first and last bytes are the
// same quote rune is a single fully quoted scalar: no interior occurrence of
// the quote closes the run before the final byte, honoring backslash escapes
// inside double quotes and doubled quotes inside single quotes. A false result
// means the outer quotes are ordinary content, so stripping them would mangle
// the value.
func quoteSpansValue(val string) bool {
	quote := val[0]
	interior := val[1 : len(val)-1]

	for i := 0; i < len(interior); i++ {
		switch {
		case quote == '"' && interior[i] == '\\':
			// A trailing backslash escapes the closing quote itself, leaving
			// the scalar unterminated.
			if i == len(interior)-1 {
				return false
			}

			i++

		case interior[i] == quote:
			// A doubled quote is the single-quote escape form; any other
			// occurrence closes the run early. A quote in the final interior
			// position pairs with the closing byte, leaving the scalar
			// unterminated.
			if quote != '\'' || i == len(interior)-1 || interior[i+1] != quote {
				return false
			}

			i++
		}
	}

	return true
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

// isYAMLNull reports whether a trimmed scalar is one of YAML's null
// spellings (null, Null, NULL, ~). The numeric paths must recognize them up
// front: goccy decodes them into float64 as 0 with no error, so a null-valued
// bound would otherwise emit a spurious 0 constraint instead of clearing.
func isYAMLNull(s string) bool {
	switch s {
	case yamlNull, "Null", "NULL", "~":
		return true
	}

	return false
}

// applyNumericBound resolves a numeric-bound annotation value onto dst using
// the given parser: an empty or null value clears the constraint (upstream's
// null-clears semantics), while an unparseable, non-finite, or out-of-domain
// value is silently skipped -- per the documented invalid-values rule -- so a
// typo on a repeated key never clears a previously set valid constraint.
func applyNumericBound[T int | float64](dst **T, val string, parse func(string) *T) {
	if s := strings.TrimSpace(val); s == "" || isYAMLNull(s) {
		*dst = nil

		return
	}

	if v := parse(val); v != nil {
		*dst = v
	}
}

// parseFloat64Ptr parses a string into *float64. Returns nil for an empty or
// null value (both clear the constraint, matching upstream and keeping the
// bare-keyword form fail-open), for unparseable values, and for non-finite
// results (.inf/.nan), which YAML accepts but JSON cannot marshal -- letting
// one through would fail the whole schema's final marshal.
func parseFloat64Ptr(val string) *float64 {
	s := strings.TrimSpace(val)
	if s == "" || isYAMLNull(s) {
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
	if s == "" || isYAMLNull(s) {
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

// parseBool parses a string as a boolean, reporting whether it parsed. An
// empty value (bare keyword with no colon) is true, matching upstream
// behavior where "# @schema required" is equivalent to
// "# @schema required:true". Only "true" and "false" (case-insensitive per
// YAML spec) are recognized; anything else reports false so callers whose
// false is an active signal (required's tri-state opt-out) can skip garbage
// instead of acting on it.
func parseBool(val string) (bool, bool) {
	val = strings.TrimSpace(val)
	if val == "" {
		return true, true
	}

	switch strings.ToLower(val) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		slog.Warn("invalid boolean for helm-values-schema annotation",
			slog.String("value", val))

		return false, false
	}
}

// parseBoolDefault parses a string as a boolean for keys whose false is an
// inert zero value: an unrecognized value is treated as false (fail open:
// don't add restrictions for garbage input).
func parseBoolDefault(val string) bool {
	b, _ := parseBool(val)

	return b
}
