// Package tagparse parses and applies the jsonschema struct tag DSL onto a
// generated [jsonschema.Schema]. The reflection generator calls [Apply] once per
// field that carries a jsonschema tag; the tag's comma-separated key=value pairs
// (or a bare description) translate into schema keywords. The keyword names are
// shared with the public package through internal/keyword, so this logic lives
// here without importing the main package.
//
// Parsing is separate from applying. [Parse] is pure: it decides whether a tag
// is key-value or a bare description, resolves the escapes, and yields the
// directives in tag order. Applying folds over those directives carrying a
// mutable shape, because a type= pair rewrites the payload mid-stream and
// changes what every later pair means; a full two-phase split would have to
// model that rewrite twice.
//
// What a directive then does to a field is not this package's: it names an
// operation from internal/tagmodel, which the validate tag interpreter names
// too, so the two dialects cannot drift on a rule they both express.
package tagparse

import (
	"fmt"
	"regexp"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
)

var (
	// Pattern matching the WORD= prefix that signals key-value mode, mirroring
	// the upstream reserved prefix (^[^ \t\n]*=).
	kvPrefixRegexp = regexp.MustCompile(`^[^ \t\n]*=`)

	// Recognized jsonschema struct tag keys. A tag enters key-value mode only
	// when its leading WORD= prefix names one of these keys; otherwise it is
	// treated as a bare description. This prevents a plain description such as
	// "a=b is the formula" from being misparsed as key-value.
	jsonSchemaTagKeys = map[string]bool{
		keyword.Description:      true,
		keyword.Title:            true,
		keyword.Type:             true,
		keyword.Pattern:          true,
		keyword.Format:           true,
		keyword.Deprecated:       true,
		keyword.ReadOnly:         true,
		keyword.WriteOnly:        true,
		keyword.UniqueItems:      true,
		keyword.Minimum:          true,
		keyword.Maximum:          true,
		keyword.ExclusiveMinimum: true,
		keyword.ExclusiveMaximum: true,
		keyword.MultipleOf:       true,
		keyword.MinLength:        true,
		keyword.MaxLength:        true,
		keyword.MinItems:         true,
		keyword.MaxItems:         true,
		keyword.MinProperties:    true,
		keyword.MaxProperties:    true,
		keyword.Default:          true,
		keyword.Const:            true,
		keyword.Enum:             true,
		keyword.Examples:         true,
	}
)

// Directive is one key=value pair of a jsonschema tag, in tag order.
type Directive struct {
	Key   string
	Value string
}

// Parse splits a jsonschema tag into its directives. A tag that is a bare
// description yields no directives and the description text instead, with the
// comma and backslash escapes resolved exactly as the key-value form resolves
// them.
func Parse(tag string) ([]Directive, string, error) {
	if tag == "" {
		return nil, "", nil
	}

	// Gate on the cheap regex before paying for splitTagPairs, then split once
	// and reuse the pairs for both the key-value decision and the directives.
	if !kvPrefixRegexp.MatchString(tag) {
		// A bare description still honors the comma/backslash escapes the
		// key=value form resolves, so "Hello\, World" becomes "Hello, World".
		// Joining the split pairs restores unescaped commas while collapsing
		// escaped ones; skip the split when there is nothing to resolve.
		if strings.ContainsRune(tag, '\\') {
			return nil, strings.Join(splitTagPairs(tag), ","), nil
		}

		return nil, tag, nil
	}

	pairs := splitTagPairs(tag)
	if !isKeyValueTag(pairs) {
		// Prose that tripped the WORD= gate but is not key=value: store the
		// escape-resolved text, not the raw tag, matching the bare path above.
		return nil, strings.Join(pairs, ","), nil
	}

	directives := make([]Directive, 0, len(pairs))

	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			return nil, "", fmt.Errorf("jsonschema tag: segment %q missing '='", pair)
		}

		if key == "" {
			return nil, "", fmt.Errorf("jsonschema tag: empty key in %q", pair)
		}

		directives = append(directives, Directive{Key: key, Value: value})
	}

	return directives, "", nil
}

// isKeyValueTag reports whether a jsonschema tag, already split into pairs by
// [splitTagPairs], should be parsed as comma-separated key=value pairs (as
// opposed to a bare description). The caller gates on [kvPrefixRegexp] first,
// so a tag with no "WORD=" prefix never reaches here.
//
// A tag is key-value when its first segment is WORD=VALUE and either the key is
// a recognized keyword, or the value is space-free. This keeps recognized
// keywords with spaced values (e.g. "description=Hello World,minimum=1") in
// key-value mode, surfaces typos like "descrption=typo" as unrecognized-key
// errors, yet treats prose such as "a=b is the formula" as a bare description.
func isKeyValueTag(pairs []string) bool {
	// An empty first segment means a malformed leading comma (e.g.
	// ",type=integer"): the kvPrefixRegexp gate matches a leading comma only when
	// a WORD= prefix precedes the first whitespace, so this is never prose. Route
	// it into the apply loop, whose missing-'=' guard reports it, instead of
	// swallowing the real constraints into the description.
	if pairs[0] == "" {
		return true
	}

	// Inspect only the first key=value segment.
	key, value, found := strings.Cut(pairs[0], "=")
	if !found {
		return false
	}

	if jsonSchemaTagKeys[key] {
		return true
	}

	// Unknown key: prose (a value containing whitespace) is a description;
	// a space-free value is a likely key=value typo that should error.
	return !strings.ContainsAny(value, " \t")
}

// splitTagPairs splits a tag string into key=value segments on unescaped
// commas. A comma can be included in a value by escaping it with a backslash
// (`\,`), and a literal backslash by doubling it (`\\`); the escapes are
// resolved in the returned segments. This lets values such as
// `description=Hello\, World` carry commas without being truncated.
func splitTagPairs(tag string) []string {
	var (
		pairs   []string
		segment strings.Builder
		escaped bool
	)

	for i := range len(tag) {
		c := tag[i]
		if escaped {
			// Preserve recognized escapes (\, and \\) literally; pass any other
			// escaped byte through unchanged so unrelated backslashes survive.
			switch c {
			case ',', '\\':
				segment.WriteByte(c)
			default:
				segment.WriteByte('\\')
				segment.WriteByte(c)
			}

			escaped = false

			continue
		}

		switch c {
		case '\\':
			escaped = true
		case ',':
			pairs = append(pairs, segment.String())
			segment.Reset()

		default:
			segment.WriteByte(c)
		}
	}

	// A trailing backslash has no following byte to escape; keep it literal.
	if escaped {
		segment.WriteByte('\\')
	}

	pairs = append(pairs, segment.String())

	return pairs
}

// splitEnumValues splits a pipe-separated list, the way this dialect spells a
// value list. The empty-segment check belongs to the caller, which knows the key
// to name in the error.
func splitEnumValues(value string) []string {
	return strings.Split(value, "|")
}
