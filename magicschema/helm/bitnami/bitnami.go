package bitnami

import (
	"regexp"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/magicschema"
)

var (
	// The key and the remaining text are captured separately; splitModifiers
	// then peels off a leading "[...]" modifier group by balanced-bracket scan,
	// which keeps an arbitrarily nested flow-sequence default (such as
	// "[array, default: [a, [b], c]]") whole. A fixed regex can only balance a
	// bounded nesting depth, so it would cut a deeper default at an inner "]"
	// and leak the rest into the description.
	paramRegex        = regexp.MustCompile(`^\s*##\s*@param\s+(\S+)\s*(.*)$`)
	skipRegex         = regexp.MustCompile(`^\s*##\s*@skip\s+(\S+)`)
	ignoredTagExpr    = regexp.MustCompile(`^\s*##\s*@(section|descriptionStart|descriptionEnd|extra)\b`)
	arrayIndexExpr    = regexp.MustCompile(`\[\d+\]`)
	trailingIndexExpr = regexp.MustCompile(`\[\d+\]$`)
)

// Annotator parses ## @param line annotations from the bitnami
// readme-generator format.
type Annotator struct {
	params map[string]*bitnamiParam
	skips  map[string]bool
}

type bitnamiParam struct {
	defaultVal  *string
	description string
	typeName    string
	nullable    bool
}

// New creates a new bitnami readme-generator annotator.
func New() *Annotator {
	return &Annotator{}
}

// Name is the canonical annotator name, used as the registry key and in
// the --annotators flag.
const Name = "bitnami"

// Name returns the annotator name.
func (a *Annotator) Name() string {
	return Name
}

// ForContent returns a new Annotator populated with parsed ## @param and
// ## @skip annotations from the given file content.
func (a *Annotator) ForContent(content []byte) (magicschema.Annotator, error) {
	clone := &Annotator{
		params: make(map[string]*bitnamiParam),
		skips:  make(map[string]bool),
	}

	lines := strings.SplitSeq(string(content), "\n")

	for line := range lines {
		// Skip recognized-but-ignored tags (@section, @descriptionStart,
		// @descriptionEnd, @extra) to avoid misparsing them as @param lines.
		if ignoredTagExpr.MatchString(line) {
			continue
		}

		// Check for @skip.
		if m := skipRegex.FindStringSubmatch(line); m != nil {
			if keyPath, ok := normalizeKeyPath(m[1]); ok {
				clone.skips[keyPath] = true
			}

			continue
		}

		// Check for @param.
		if m := paramRegex.FindStringSubmatch(line); m != nil {
			keyPath, ok := normalizeKeyPath(m[1])
			if !ok {
				continue
			}

			modifiers, description := splitModifiers(m[2])

			param := &bitnamiParam{
				description: description,
			}

			// No guard needed: parseModifiers is a no-op on an empty modifier
			// group.
			parseModifiers(param, modifiers)

			clone.params[keyPath] = param
		}
	}

	return clone, nil
}

// Annotate looks up the key path in the pre-built annotation map.
func (a *Annotator) Annotate(_ ast.Node, keyPath string) *magicschema.AnnotationResult {
	// Check for skip.
	if a.skips[keyPath] {
		return &magicschema.AnnotationResult{Skip: true}
	}

	param, ok := a.params[keyPath]
	if !ok {
		return nil
	}

	schema := &jsonschema.Schema{
		Description: param.description,
	}

	// Build the type list and let SetSchemaType pick scalar vs union (and dedup),
	// like the other annotators, rather than hand-rolling the cases. A
	// nullable-only param collapses to the scalar "null" type.
	var types []string

	if param.typeName != "" {
		types = append(types, param.typeName)
	}

	if param.nullable {
		types = append(types, "null")
	}

	magicschema.SetSchemaType(schema, types)

	if param.defaultVal != nil {
		schema.Default = magicschema.ParseYAMLValue(*param.defaultVal)
	}

	return &magicschema.AnnotationResult{Schema: schema}
}

// normalizeKeyPath strips array indices from a bitnami key path, converting
// paths like "jobs[0].nameOverride" to "jobs.nameOverride" so annotations
// match the dot-separated key paths used by the generator's AST walker.
//
// A key path whose final segment is a bare positional index (such as
// "items[0]") targets a single array element rather than a key the walker
// visits. Stripping that index would attach the element's type, default, or
// skip to the array key itself, producing a schema that rejects the array
// value present in the source. The second return value reports whether the
// path resolves to a walker key; element-level paths report false and the
// annotation is dropped (fail open).
func normalizeKeyPath(keyPath string) (string, bool) {
	if trailingIndexExpr.MatchString(keyPath) {
		return "", false
	}

	return arrayIndexExpr.ReplaceAllString(keyPath, ""), true
}

// splitModifiers separates a leading "[...]" modifier group from the
// description that follows it. It scans for the bracket that balances the
// opening "[", so a default value that is itself a nested flow sequence
// ("[array, default: [a, [b], c]]") stays whole instead of being cut at the
// first inner "]". When the brackets do not balance, the whole text is taken
// as the description (no modifiers), failing open.
func splitModifiers(rest string) (string, string) {
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "[") {
		return "", rest
	}

	depth := 0

	for i, ch := range rest {
		switch ch {
		case '[':
			depth++
		case ']':
			depth--

			if depth == 0 {
				return rest[1:i], strings.TrimSpace(rest[i+1:])
			}
		}
	}

	return "", rest
}

// parseModifiers parses the comma-separated modifiers within brackets.
//
// The default: modifier takes the remainder of the bracket contents rather than
// a single comma-delimited token, so a default value containing commas (e.g.
// "[array, default: a,b,c]") is preserved instead of truncated at the first
// comma. This follows the bitnami convention of writing default: last; the
// modifiers before it are the type and nullable flags. The marker is matched at
// a comma boundary (see [defaultModifierIndex]) so a token such as
// "somedefault:" cannot stand in for it.
func parseModifiers(param *bitnamiParam, modifiers string) {
	if idx := defaultModifierIndex(modifiers); idx >= 0 {
		val := strings.TrimSpace(modifiers[idx+len("default:"):])

		// An empty "[default:]" carries no value; setting it would emit a
		// spurious "default": null.
		if val != "" {
			param.defaultVal = &val
		}

		modifiers = modifiers[:idx]
	}

	for part := range strings.SplitSeq(modifiers, ",") {
		part = strings.TrimSpace(part)

		switch part {
		case "nullable":
			param.nullable = true
		case "string", "array", "object", "number", "integer", "boolean":
			param.typeName = part
		}

		// Unknown modifiers silently ignored (best-effort).
	}
}

// defaultModifierIndex returns the byte offset of the "default:" modifier --
// the marker at the start of the bracket contents or directly after a comma,
// ignoring surrounding spaces -- or -1 when it is absent. Anchoring to a comma
// boundary keeps another token such as "somedefault:" from matching as a
// substring, while still letting the default value itself contain commas (the
// modifier takes the remainder of the bracket).
func defaultModifierIndex(modifiers string) int {
	const marker = "default:"

	for i := 0; i+len(marker) <= len(modifiers); i++ {
		if modifiers[i:i+len(marker)] != marker {
			continue
		}

		j := i - 1
		for j >= 0 && (modifiers[j] == ' ' || modifiers[j] == '\t') {
			j--
		}

		if j < 0 || modifiers[j] == ',' {
			return i
		}
	}

	return -1
}
