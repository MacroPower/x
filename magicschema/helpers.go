package magicschema

import (
	"encoding/json"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/google/jsonschema-go/jsonschema"
)

// DefaultValue converts a Go value to a [json.RawMessage] suitable for use
// as a JSON Schema default value. Returns nil if marshaling fails.
func DefaultValue(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}

	return b
}

// ConstValue converts a Go value to a pointer-to-any suitable for use as a
// JSON Schema const value. It returns nil when the value cannot be marshaled
// to JSON -- a NaN or infinite float, which encoding/json rejects -- so a
// non-finite const never reaches the schema and breaks its final marshal.
func ConstValue(v any) *any {
	if DefaultValue(v) == nil {
		return nil
	}

	return new(v)
}

// FilterJSONSafe returns the values that can be marshaled to JSON, dropping any
// that carry a NaN or infinite float anywhere in their structure -- values that
// encoding/json rejects, so one left in an enum or examples list would break the
// whole schema's final marshal. The result is nil when no value survives, which
// clears the constraint (fail-open) rather than emitting an empty enum that
// validates nothing.
func FilterJSONSafe(vals []any) []any {
	var out []any

	for _, v := range vals {
		if DefaultValue(v) != nil {
			out = append(out, v)
		}
	}

	return out
}

// TrueSchema returns a schema that validates everything (marshals to JSON true).
func TrueSchema() *jsonschema.Schema {
	return &jsonschema.Schema{}
}

// FalseSchema returns a schema that validates nothing (marshals to JSON false).
func FalseSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Not: &jsonschema.Schema{}}
}

// ToSubSchema converts an arbitrary Go value (a map[string]any, bool, or any
// other JSON-marshalable value) to a [*jsonschema.Schema] by round-tripping
// through JSON. Returns nil for values that do not survive the round trip
// (annotation parse failures are skipped, never fatal). The round trip
// includes marshaling back out: the jsonschema marshaler validates its
// invariants only on marshal, so a decoded schema carrying a combination it
// rejects -- both definitions and $defs anywhere in the tree -- is returned
// as nil (the annotation is skipped, fail open) rather than passed through
// to break the whole document's final marshal. Type arrays anywhere
// in the tree are normalized with [SetSchemaType] semantics: YAML nulls
// (e.g. "type: [null, string]") become the "null" type string, matching how
// annotators translate the YAML null literal at the top level; duplicate
// members drop; a single remaining member collapses to the scalar Type; and
// an empty array leaves the type unset.
func ToSubSchema(val any) *jsonschema.Schema {
	if val == nil {
		return nil
	}

	b, err := json.Marshal(val)
	if err != nil {
		return nil
	}

	var schema jsonschema.Schema

	err = json.Unmarshal(b, &schema)
	if err != nil {
		return nil
	}

	normalizeTypes(&schema)

	// The jsonschema marshaler runs its invariant checks (basicChecks) only in
	// MarshalJSON, never on unmarshal, so a decoded schema can carry a
	// marshal-fatal combination such as both definitions and $defs. The value
	// receiver on MarshalJSON means a document marshal checks every nested
	// sub-schema, so one bad annotation would fail the entire output. Probing
	// the marshal here surfaces the rejection while it is still skippable.
	_, err = json.Marshal(&schema)
	if err != nil {
		return nil
	}

	return &schema
}

// normalizeTypes normalizes Types across the schema tree so a round-tripped
// sub-schema upholds the same type invariants [SetSchemaType] enforces for
// annotation-supplied lists. A YAML null inside a type array survives the
// JSON round trip as an empty string in Types, which is not a valid JSON
// Schema type; it is rewritten to the "null" type. The list then passes
// through [SetSchemaType], so duplicates drop (a type array must have unique
// members), a single member collapses to the scalar Type, and an empty array
// ("type: []") leaves the type unset -- nil Types -- instead of emitting the
// invalid "type": []. Walking the typed tree keeps non-schema values
// (defaults, enums, consts) untouched.
func normalizeTypes(s *jsonschema.Schema) {
	if s == nil {
		return
	}

	if s.Types != nil {
		types := s.Types
		// SetSchemaType leaves the schema untouched for an empty list, so clear
		// Types first: a zero-length array normalizes to nil rather than
		// surviving as a non-nil empty slice.
		s.Types = nil

		for i, t := range types {
			if t == "" {
				types[i] = typeNull
			}
		}

		SetSchemaType(s, types)
	}

	forEachSubSchema(s, normalizeTypes)
}

// forEachSubSchema calls fn on each non-nil direct sub-schema of s -- the
// single-schema fields, the schema slices, and the schema maps. It does not
// recurse; a caller walking the whole tree calls forEachSubSchema again from
// within fn. The enumerated field set mirrors the sub-schema shape of
// jsonschema.Schema and is the one place the package spells it out, so a later
// tree walk reuses it instead of copying the list.
func forEachSubSchema(s *jsonschema.Schema, fn func(*jsonschema.Schema)) {
	for _, sub := range [...]*jsonschema.Schema{
		s.Items, s.AdditionalItems, s.Contains, s.UnevaluatedItems,
		s.AdditionalProperties, s.PropertyNames, s.UnevaluatedProperties,
		s.Not, s.If, s.Then, s.Else, s.ContentSchema,
	} {
		if sub != nil {
			fn(sub)
		}
	}

	for _, subs := range [...][]*jsonschema.Schema{
		s.PrefixItems, s.ItemsArray, s.AllOf, s.AnyOf, s.OneOf,
	} {
		for _, sub := range subs {
			if sub != nil {
				fn(sub)
			}
		}
	}

	for _, subs := range [...]map[string]*jsonschema.Schema{
		s.Defs, s.Definitions, s.DependencySchemas, s.Properties,
		s.PatternProperties, s.DependentSchemas,
	} {
		for _, sub := range subs {
			if sub != nil {
				fn(sub)
			}
		}
	}
}

// ToSubSchemaArray converts a []any to []*jsonschema.Schema. Conversion is
// all-or-nothing: if any element does not survive the round trip (or the list
// is empty), the whole result is nil. This suits the combinator keywords
// (anyOf/oneOf/allOf) it feeds, where silently dropping one branch would change
// the keyword's cardinality -- an anyOf or oneOf would lose an alternative and
// reject values it should accept (fail closed) -- so clearing the keyword
// entirely is the fail-open choice.
func ToSubSchemaArray(val any) []*jsonschema.Schema {
	arr, ok := val.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}

	schemas := make([]*jsonschema.Schema, 0, len(arr))

	for _, item := range arr {
		s := ToSubSchema(item)
		if s == nil {
			return nil
		}

		schemas = append(schemas, s)
	}

	return schemas
}

// ToSubSchemaMap converts a map[string]any to map[string]*jsonschema.Schema.
func ToSubSchemaMap(val any) map[string]*jsonschema.Schema {
	m, ok := val.(map[string]any)
	if !ok {
		return nil
	}

	result := make(map[string]*jsonschema.Schema, len(m))

	for key, v := range m {
		s := ToSubSchema(v)
		if s != nil {
			result[key] = s
		}
	}

	return result
}

// ParseYAMLValue parses a YAML value string into [json.RawMessage]. A blank
// (empty or whitespace-only) input returns nil rather than the JSON null
// [DefaultValue] would produce for it: a blank value is the absence of a value,
// not an explicit null, so an annotation default left empty must not advertise
// a null default. An explicit null is written out ("null") and still parses.
func ParseYAMLValue(val string) json.RawMessage {
	if strings.TrimSpace(val) == "" {
		return nil
	}

	var v any

	err := yaml.Unmarshal([]byte(val), &v)
	if err != nil {
		return nil
	}

	return DefaultValue(v)
}

// LastCommentGroup returns the lines of the final comment group: the lines
// after the last blank comment line, ignoring trailing blanks. A line is
// blank when stripping '#' markers and whitespace leaves nothing. Annotation
// formats scope to the comment group directly above a key, so earlier groups
// (often documentation for the file or a preceding key) are excluded. The
// returned slice contains no blank lines.
func LastCommentGroup(lines []string) []string {
	blank := func(line string) bool {
		stripped := strings.TrimSpace(line)
		stripped = strings.TrimLeft(stripped, "#")

		return strings.TrimSpace(stripped) == ""
	}

	end := len(lines)
	for end > 0 && blank(lines[end-1]) {
		end--
	}

	lastBlank := -1

	for i, line := range lines[:end] {
		if blank(line) {
			lastBlank = i
		}
	}

	return lines[lastBlank+1 : end]
}

// StripCommentMarker removes leading whitespace, up to two leading '#'
// characters, and a single following space from a comment line. Capping the
// hashes at two is how block markers such as "# @schema" and "## @param" are
// recognized, so a line with three or more hashes ("### @schema") is treated
// as prose, not a marker. Keeping only one trailing space means deeper
// indentation after "# " survives for nested YAML block content. Annotators
// and the structural fence detector strip markers with this helper so a
// marker is recognized consistently across the package.
func StripCommentMarker(line string) string {
	line = strings.TrimSpace(line)

	for range 2 {
		line = strings.TrimPrefix(line, "#")
	}

	return strings.TrimPrefix(line, " ")
}

// SetSchemaType assigns a parsed type list to a schema as either the scalar
// Type or the Types union, clearing the sibling field so the schema never
// carries both -- a combination the jsonschema marshaler rejects, which would
// break the whole document's final marshal. An empty list leaves Type and Types
// unset, so structural inference and the fail-open default still apply.
//
// Repeated entries are dropped while first-seen order is preserved: a JSON
// Schema type array must have unique members, so a duplicate (type:
// [string, string], or a type written twice across repeated pairs) collapses to
// the scalar Type rather than emitting an array the spec rejects.
func SetSchemaType(s *jsonschema.Schema, types []string) {
	if len(types) > 1 {
		seen := make(map[string]struct{}, len(types))
		deduped := make([]string, 0, len(types))

		for _, t := range types {
			if _, ok := seen[t]; ok {
				continue
			}

			seen[t] = struct{}{}

			deduped = append(deduped, t)
		}

		types = deduped
	}

	switch len(types) {
	case 0:
		// Empty or unparseable value; leave Type and Types unset.
	case 1:
		s.Type = types[0]
		s.Types = nil

	default:
		s.Type = ""
		s.Types = types
	}
}
