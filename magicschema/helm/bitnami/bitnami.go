package bitnami

import (
	"regexp"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/internal/yamldoc"
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

	// Block scalar interiors are blanked before the scan: string data inside
	// a "|" or ">" value may look exactly like a ## @param line, and
	// registering it would attach a wrong type or description to a real key
	// (fail closed).
	for _, line := range yamldoc.MaskBlockScalars(content) {
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
	param, ok := a.params[keyPath]

	// A @skip omits the subtree only when no @param documents the same key
	// path. Upstream filters skipped entries out of its own render list, but
	// a separate @param entry for the key still renders into the schema, so
	// the param wins the conflict here too -- dropping the key, its type,
	// and its whole inferred subtree over a stale @skip would fail closed.
	if !ok {
		if a.skips[keyPath] {
			return &magicschema.AnnotationResult{Skip: true}
		}

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
// Tokens are split on commas at bracket depth zero, mirroring the upstream
// comma-splitting: modifiers written after default: (an ordering upstream
// accepts, e.g. "[default: abc, nullable]") are applied rather than swallowed
// into the default value. The depth-zero rule additionally keeps a bracketed
// flow-sequence default such as "[array, default: [a, [b], c]]" whole, an
// input upstream cannot express (see Intentional Divergences in doc.go). A
// bare comma inside a default value ends it, matching upstream; the leftover
// tokens are unknown modifiers and are silently ignored (best-effort). The
// default: marker must start its token, so "somedefault:" cannot stand in
// for it.
func parseModifiers(param *bitnamiParam, modifiers string) {
	for _, part := range splitModifierTokens(modifiers) {
		part = strings.TrimSpace(part)

		if val, ok := strings.CutPrefix(part, "default:"); ok {
			val = strings.TrimSpace(val)

			// An empty "[default:]" carries no value; setting it would emit a
			// spurious "default": null.
			if val != "" {
				param.defaultVal = &val
			}

			continue
		}

		switch part {
		case "nullable":
			param.nullable = true
		case "string", "array", "object", "number", "integer", "boolean":
			param.typeName = part
		}

		// Unknown modifiers silently ignored (best-effort).
	}
}

// splitModifierTokens splits bracket contents on commas at bracket depth
// zero, so a comma inside a nested flow sequence (e.g. the default value in
// "array, default: [a, [b], c]") does not end its token. The input is the
// balanced group peeled by [splitModifiers], so the depth never goes
// negative.
func splitModifierTokens(modifiers string) []string {
	var (
		tokens []string
		depth  int
		start  int
	)

	for i, ch := range modifiers {
		switch ch {
		case '[':
			depth++
		case ']':
			depth--
		case ',':
			if depth == 0 {
				tokens = append(tokens, modifiers[start:i])
				start = i + 1
			}
		}
	}

	return append(tokens, modifiers[start:])
}
