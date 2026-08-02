package validate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// ErrConflictingConstraints reports two tag rules on one field that can never
// both hold, such as required and eq=false on a bool: required means the value
// must be true while eq=false pins it to false, so no value satisfies both. It
// derives from the package-level [jsonschema.ErrConstraintConflict], so a
// conflict this interpreter raises is recognizable through either sentinel.
var (
	ErrConflictingConstraints = fmt.Errorf(
		"validate tag: conflicting constraints: %w",
		jsonschema.ErrConstraintConflict,
	)

	// The oneOfSplitRegexp pattern matches one oneof token, mirroring
	// go-playground/validator's own splitter (`'[^']*'|\S+`): a single-quoted
	// run (one value even with spaces) or an unquoted whitespace-delimited run.
	// A quote only opens a group when it is the first character of a token; an
	// interior, trailing, or unbalanced quote is matched by the \S+ alternative
	// and stripped afterward.
	oneOfSplitRegexp = regexp.MustCompile(`'[^']*'|\S+`)
)

// Interpreter implements [jsonschema.TagInterpreter] for go-playground/validator
// tag syntax. Create one with [NewInterpreter] and register it under the
// "validate" tag key:
//
//	jsonschema.WithTagInterpreter("validate", validate.NewInterpreter())
//
// The interpreter owns this dialect's grammar and nothing else. Splitting the
// tag, stripping the OR alternatives, unescaping a parameter, tracking dive and
// keys blocks, and skipping the control tags all live here; which operation a
// key names lives in the key table, and what that operation does to a given
// field shape lives in the shared constraint model. There is no parse or
// emission path of its own: every constraint is contributed through
// [jsonschema.Constraints], so this package writes no schema keyword directly.
type Interpreter struct{}

// NewInterpreter returns a new validate tag interpreter.
func NewInterpreter() *Interpreter {
	return &Interpreter{}
}

// Interpret parses the validate tag value from [jsonschema.Tag] and declares
// constraints on the field's authored canvas. Interpretation is pure tag
// parsing, so the context is unused.
func (i *Interpreter) Interpret(_ context.Context, field jsonschema.FieldContext, tag jsonschema.Tag) error {
	// Split on commas first, exactly as go-playground/validator does; the OR
	// operator is then handled per comma group inside applyParts. Splitting on
	// the first pipe up front would discard every later comma-separated
	// constraint (e.g. "oneof=a|b,required" would drop required).
	parts := strings.Split(tag.Value, ",")

	return applyParts(parts, field)
}

// applyParts applies a sequence of validator tag parts to a field.
func applyParts(parts []string, field jsonschema.FieldContext) error {
	var inKeys bool

	for idx := range parts {
		part := parts[idx]
		// The | OR operator is not modeled. The go-playground/validator parser
		// splits a comma group on the pipe and treats the alternatives as OR;
		// here only the first alternative (the group before the first pipe) is
		// interpreted, matching the documented behavior. Stripping per part
		// rather than across the whole tag keeps later comma-separated
		// constraints intact. A literal pipe in a param is written 0x7C and
		// survives, since unescapeParam runs after this split.
		if i := strings.IndexByte(part, '|'); i >= 0 {
			part = part[:i]
		}

		part = strings.TrimSpace(part)
		if part == "" || part == "-" {
			continue
		}

		// A dive inside a keys...endkeys block is a key-side dive (e.g.
		// dive,keys,dive,endkeys for collection-typed map keys), which is not
		// modeled; it must be skipped by the inKeys guard below rather than
		// treated as a value-element dive. Only handle dive outside the block.
		if part == "dive" && !inKeys {
			// Descend into element type. A trailing dive with no subsequent
			// constraints is an error (matches go-playground/validator).
			remaining := parts[idx+1:]
			if !hasConstraint(remaining) {
				return fmt.Errorf("validate tag: dive with no subsequent constraints")
			}

			return applyDive(remaining, field)
		}

		key, value, hasValue := strings.Cut(part, "=")
		if hasValue {
			// Tags split blindly on commas, pipes, and equals, then the
			// documented escapes in the param value only are unescaped:
			// "0x2C" -> "," and "0x7C" -> "|". This lets a param carry a literal
			// comma or pipe (e.g. oneof=a0x2Cb yields the enum value "a,b"). The
			// key is never unescaped, matching go-playground/validator cache.go.
			value = unescapeParam(value)
		}

		// Skip cross-field validators, and the control tags that govern when
		// validation runs rather than expressing a value constraint (e.g.
		// omitempty, structonly). Neither has a schema representation, and
		// neither may be treated as an unknown validator.
		if isCrossFieldValidator(key) || isControlTag(key) {
			continue
		}

		// Map key validators: constraints between keys and endkeys apply to
		// the map's keys (not modeled here) and are skipped. A keys without a
		// matching endkeys is malformed; rather than swallowing every later
		// constraint, the keys marker is ignored so the remaining constraints
		// still apply to the value schema.
		if key == "keys" {
			if hasEndkeys(parts[idx+1:]) {
				inKeys = true
			}

			continue
		}

		if key == "endkeys" {
			inKeys = false
			continue
		}

		if inKeys {
			continue
		}

		err := applyValidator(key, value, hasValue, field)
		if err != nil {
			return err
		}
	}

	return nil
}

// applyValidator applies one validator to the field: look the key up in this
// dialect's table, bind its parameter against the row's declared arity, and hand
// the resulting rule to the shared model. Everything the rule then does -- which
// keyword family it targets, whether its scalar compares against the Go value or
// the serialized text, whether it retargets onto element schemas -- is decided
// there from the field's shape.
//
// The one thing that is not a field constraint stays here: required also adds
// the field to its parent object's required list, which is a write on the
// enclosing schema rather than on this field.
func applyValidator(key, value string, hasValue bool, field jsonschema.FieldContext) error {
	rule, known := validatorKeys[key]
	if !known {
		return fmt.Errorf("validate tag: unrecognized validator %q", key)
	}

	if rule.Op == tagmodel.OpNonZero && field.Parent != nil && field.Name != "" {
		addRequired(field.Parent, field.Name)
	}

	shape := shapeOf(field)

	bound, err := tagmodel.Bind(rule.KeyRule, shape, value, hasValue)
	if err != nil {
		// A row carrying its own note replaces the generic arity reason, but
		// still wraps the model's error, so errors.Is keeps working through it.
		reason := key
		if rule.paramNote != "" {
			reason = fmt.Sprintf(rule.paramNote, value)
		}

		return fmt.Errorf("validate tag: %s: %w", reason, err)
	}

	err = field.ConstraintsFor(shape).Apply(bound.Op, bound.Axis, bound.Params.Values()...)
	if err != nil {
		return wrapApplyError(key, value, err)
	}

	return nil
}

// wrapApplyError gives a model error this dialect's phrasing. A conflict is
// re-reported through this package's own sentinel, so the identity
// [ErrConflictingConstraints] promises holds through every layer; everything
// else keeps the model's reason, which is the one place that rejection is
// written.
//
// The model reports what happened -- a different value is already pinned, an
// enumeration is already set -- and this names the tag that tried to add one, so
// the message says which rule to look at without every rule carrying its own
// conflict check. The sentinel's own prefix is stripped from the model's text so
// the composed message reads as one sentence rather than saying "conflicting
// value constraints" twice.
func wrapApplyError(key, value string, err error) error {
	if !errors.Is(err, jsonschema.ErrConstraintConflict) {
		return fmt.Errorf("validate tag: %s: %w", key, err)
	}

	rule := key
	if value != "" {
		rule = key + "=" + value
	}

	reason := err.Error()
	if _, stripped, found := strings.Cut(reason, jsonschema.ErrConstraintConflict.Error()+": "); found {
		reason = stripped
	}

	return fmt.Errorf("%w: %s conflicts with a constraint already in force: %s",
		ErrConflictingConstraints, rule, reason)
}

// unescapeParam applies go-playground/validator's documented param escapes:
// "0x2C" becomes a literal comma and "0x7C" a literal pipe. Tags are split on
// commas and pipes before parsing, so these escapes are the only way a param
// value can contain either character. The order matches validator cache.go:
// commas are unescaped before pipes.
func unescapeParam(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "0x2C", ","), "0x7C", "|")
}

// hasConstraint reports whether parts contains at least one meaningful
// (non-empty, non-skip) constraint.
func hasConstraint(parts []string) bool {
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "-" {
			continue
		}

		// A control tag such as omitempty or structonly, or a cross-field
		// validator, governs when validation runs rather than constraining a
		// value, so it does not satisfy a trailing dive. Match on the key before
		// any equals sign.
		key, _, _ := strings.Cut(p, "=")
		if isControlTag(key) || isCrossFieldValidator(key) {
			continue
		}

		return true
	}

	return false
}

// hasEndkeys reports whether parts contains an "endkeys" marker.
func hasEndkeys(parts []string) bool {
	for _, p := range parts {
		if strings.TrimSpace(p) == "endkeys" {
			return true
		}
	}

	return false
}

// addRequired adds a field to the parent's required list if not already present.
func addRequired(parent *jsonschema.Schema, name string) {
	if slices.Contains(parent.Required, name) {
		return
	}

	parent.Required = append(parent.Required, name)
}

// splitOneOfValues tokenizes a oneof tag value the way go-playground/validator
// does: whitespace separates values, but a single-quoted run is one value even
// when it contains spaces, and every quote in each token is then stripped. So
// "oneof='New York' Boston" yields ["New York", "Boston"] rather than being
// shattered on every space, and "oneof=ab'cd ef" yields ["abcd", "ef"] -- the
// interior quote does not suppress the separator -- matching the upstream
// tokenize-then-strip exactly.
func splitOneOfValues(value string) []string {
	out := oneOfSplitRegexp.FindAllString(value, -1)
	for i := range out {
		out[i] = strings.ReplaceAll(out[i], "'", "")
	}

	return out
}
