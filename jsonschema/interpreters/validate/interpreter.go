package validate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
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

	// ErrUnrecognizedValidator reports a key this dialect does not express: a
	// typo, or a go-playground validator with no constraint in the shared
	// model, such as isdefault. The error names the key. A caller comparing
	// this interpreter against go-playground reads it as the documented
	// vocabulary gap rather than a refusal of the tag.
	ErrUnrecognizedValidator = errors.New("validate tag: unrecognized validator")

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
		part := strings.TrimSpace(parts[idx])
		if part == "" || part == "-" {
			continue
		}

		// The parts go-playground matches whole come first, before the OR
		// split, since its parser switches on the whole comma group and only
		// the default branch splits on the pipe. A dive inside a
		// keys...endkeys block is a key-side dive (e.g. dive,keys,dive,endkeys
		// for collection-typed map keys), which is not modeled; it must be
		// skipped by the inKeys guard below rather than treated as a
		// value-element dive. Only handle dive outside the block.
		if part == diveTag && !inKeys {
			// Descend into the element type. A trailing dive applies nothing
			// to the elements, a no-op as in go-playground, but the descent
			// itself still needs elements to reach: go-playground panics on a
			// dive over anything but a slice, array, or map, so a dive on a
			// field with no elements is an error here whatever follows it.
			return applyDive(parts[idx+1:], field)
		}

		// Map key validators: constraints between keys and endkeys apply to
		// the map's keys (not modeled here) and are skipped. A keys with no
		// endkeys runs to the end of the tag, as it does in go-playground,
		// which collects every later part into the key block; applying them
		// to the value schema instead would emit a constraint the tag never
		// places there.
		if part == keysTag {
			inKeys = true

			continue
		}

		if part == endkeysTag {
			inKeys = false
			continue
		}

		// A control tag governs when validation runs rather than expressing
		// a value constraint (e.g. omitempty, structonly). It has no schema
		// representation and may not be treated as an unknown validator. It
		// is matched as the whole part, as go-playground matches it, so a
		// parameter on one (omitempty=) or an OR alternative beside it
		// (omitempty|min=1) is refused below rather than skipped.
		if isControlTag(part) {
			continue
		}

		// The | OR operator is not modeled. The go-playground/validator parser
		// splits a comma group on the pipe and treats the alternatives as OR;
		// here only the first alternative (the group before the first pipe) is
		// interpreted, matching the documented behavior. Stripping per part
		// rather than across the whole tag keeps later comma-separated
		// constraints intact. A literal pipe in a param is written 0x7C and
		// survives, since unescapeParam runs after this split.
		orGroup := false

		if i := strings.IndexByte(part, '|'); i >= 0 {
			// An empty first alternative is a tag go-playground refuses
			// outright; dropping the whole group would silently weaken the
			// schema instead.
			if strings.TrimSpace(part[:i]) == "" {
				return fmt.Errorf("validate tag: empty OR alternative in %q", part)
			}

			orGroup = true
			part = strings.TrimSpace(part[:i])
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

		// Skip cross-field validators, which have no schema representation
		// and may not be treated as an unknown validator. Go-playground
		// registers them, so one inside an OR group is as valid there as a
		// bare one.
		if isCrossFieldValidator(key) {
			continue
		}

		if inKeys {
			continue
		}

		// A structural key carrying a parameter or standing as an OR
		// alternative names no validator on either side: go-playground looks
		// it up as a validator there and refuses the tag. The error carries
		// the whole part, as go-playground's does.
		if isStructuralKey(key) && (hasValue || orGroup) {
			return fmt.Errorf("%w %q", ErrUnrecognizedValidator, part)
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
		return fmt.Errorf("%w %q", ErrUnrecognizedValidator, key)
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

	if bound.Op == tagmodel.OpOneOf {
		err = checkCanonicalOneOf(field, shape, bound.Params.Values())
		if err != nil {
			return fmt.Errorf("validate tag: %s: %w", key, err)
		}
	}

	err = field.ConstraintsFor(shape).Apply(bound.Op, bound.Axis, bound.Params.Values()...)
	if err != nil {
		return wrapApplyError(key, value, err)
	}

	return nil
}

// checkCanonicalOneOf rejects a oneof token that go-playground could never
// match on the number the rule reaches. On a sequence the rule retargets onto
// the elements, so the check descends through the element contexts the same
// way and runs on every numeric leaf; a oneof written on a []float64 and one
// written under a dive then refuse the same tokens. A coerced number is
// exempt: its scalars are canonicalized against the serialized text by design
// (see the package doc).
func checkCanonicalOneOf(field jsonschema.FieldContext, shape tagmodel.Shape, tokens []string) error {
	if shape.Form == tagmodel.FormNumber {
		return checkCanonicalOneOfKind(shape.Kind, tokens)
	}

	if shape.Form != tagmodel.FormArray {
		return nil
	}

	elems := field.ElementContexts()
	for i := range elems {
		err := checkCanonicalOneOf(elems[i], shapeOf(elems[i]), tokens)
		if err != nil {
			return err
		}
	}

	return nil
}

// checkCanonicalOneOfKind rejects a oneof token on a numeric kind whose
// spelling go-playground could never match. Its isOneOf compares the field's
// value formatted with strconv against the raw tokens, so a token such as +1,
// 01, or 1.0 matches no value at all, while the enum this dialect emits would
// admit the number it parses to. A token that does not parse at the kind's
// width is left to the model, which reports the parse or range fault.
func checkCanonicalOneOfKind(kind reflect.Kind, tokens []string) error {
	bits := kindBits(kind)

	for _, tok := range tokens {
		var canonical string

		switch {
		case numkind.IsUnsigned(kind):
			n, err := strconv.ParseUint(tok, 10, bits)
			if err != nil {
				continue
			}

			canonical = strconv.FormatUint(n, 10)

		case numkind.IsInteger(kind):
			n, err := strconv.ParseInt(tok, 10, bits)
			if err != nil {
				continue
			}

			canonical = strconv.FormatInt(n, 10)

		case numkind.IsFloat(kind):
			f, err := strconv.ParseFloat(tok, bits)
			if err != nil {
				continue
			}

			// The shortest decimal at the kind's width, as the package doc
			// promises for every float literal; formatting a float32 at 64
			// bits would refuse every token the width cannot hold exactly.
			canonical = strconv.FormatFloat(f, 'f', -1, bits)

		default:
			return nil
		}

		if canonical != tok {
			return fmt.Errorf("%q is not the canonical spelling %q go-playground compares against", tok, canonical)
		}
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
		// value, so it does not satisfy a trailing dive. A control tag is the
		// whole part; a cross-field validator is the key before its parameter.
		key, _, _ := strings.Cut(p, "=")
		if isControlTag(p) || isCrossFieldValidator(key) {
			continue
		}

		return true
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

// kindBits returns the bit width a numeric kind parses at, 64 for the
// platform-sized and 64-bit kinds.
func kindBits(kind reflect.Kind) int {
	switch kind {
	case reflect.Int8, reflect.Uint8:
		return 8
	case reflect.Int16, reflect.Uint16:
		return 16
	case reflect.Int32, reflect.Uint32, reflect.Float32:
		return 32
	default:
		return 64
	}
}

// splitOneOfValues tokenizes a oneof tag value the way go-playground/validator
// does: whitespace separates values, but a single-quoted run is one value even
// when it contains spaces, and every quote in each token is then stripped. So
// "oneof='New York' Boston" yields ["New York", "Boston"] rather than being
// shattered on every space, and "oneof=ab'cd ef" yields ["abcd", "ef"] -- the
// interior quote does not suppress the separator -- matching the upstream
// tokenize-then-strip exactly.
func splitOneOfValues(value string) []string {
	found := oneOfSplitRegexp.FindAllString(value, -1)

	// A value listed twice enumerates once; the enum is a set.
	out := make([]string, 0, len(found))
	seen := make(map[string]bool, len(found))

	for _, tok := range found {
		tok = strings.ReplaceAll(tok, "'", "")
		if seen[tok] {
			continue
		}

		seen[tok] = true
		out = append(out, tok)
	}

	return out
}
