package bitnami

import (
	"regexp"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/magicschema"
)

var (
	paramRegex     = regexp.MustCompile(`^\s*##\s*@param\s+(\S+)\s*(?:\[(.*?)\])?\s*(.*)$`)
	skipRegex      = regexp.MustCompile(`^\s*##\s*@skip\s+(\S+)`)
	ignoredTagExpr = regexp.MustCompile(`^\s*##\s*@(section|descriptionStart|descriptionEnd|extra)\b`)
	arrayIndexExpr = regexp.MustCompile(`\[\d+\]`)
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

// Name returns the annotator name.
func (a *Annotator) Name() string {
	return "bitnami"
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
			clone.skips[normalizeKeyPath(m[1])] = true

			continue
		}

		// Check for @param.
		if m := paramRegex.FindStringSubmatch(line); m != nil {
			keyPath := normalizeKeyPath(m[1])
			modifiers := m[2]
			description := strings.TrimSpace(m[3])

			param := &bitnamiParam{
				description: description,
			}

			if modifiers != "" {
				parseModifiers(param, modifiers)
			}

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

	if param.typeName != "" {
		if param.nullable {
			schema.Types = []string{param.typeName, "null"}
		} else {
			schema.Type = param.typeName
		}
	} else if param.nullable {
		schema.Types = []string{"null"}
	}

	if param.defaultVal != nil {
		schema.Default = magicschema.ParseYAMLValue(*param.defaultVal)
	}

	return &magicschema.AnnotationResult{Schema: schema}
}

// normalizeKeyPath strips array indices from a bitnami key path, converting
// paths like "jobs[0].nameOverride" to "jobs.nameOverride".
func normalizeKeyPath(keyPath string) string {
	return arrayIndexExpr.ReplaceAllString(keyPath, "")
}

// parseModifiers parses the comma-separated modifiers within brackets.
func parseModifiers(param *bitnamiParam, modifiers string) {
	parts := strings.SplitSeq(modifiers, ",")

	for part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		switch {
		case part == "nullable":
			param.nullable = true
		case part == "string":
			param.typeName = "string"
		case part == "array":
			param.typeName = "array"
		case part == "object":
			param.typeName = "object"
		case part == "number":
			param.typeName = "number"
		case part == "integer":
			param.typeName = "integer"
		case part == "boolean":
			param.typeName = "boolean"
		case strings.HasPrefix(part, "default:"):
			val := strings.TrimPrefix(part, "default:")
			val = strings.TrimSpace(val)
			param.defaultVal = &val
		}
		// Unknown modifiers silently ignored (best-effort).
	}
}
