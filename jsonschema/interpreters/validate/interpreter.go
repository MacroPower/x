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
	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
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

	// ErrKeysPlacement reports a keys tag go-playground cannot open a key
	// block on: one that does not immediately follow a dive, inside a block
	// as much as outside one, or one whose dive descends into a slice or
	// array. Go-playground's parser refuses the first, and only its map
	// branch reads a block, so the second dereferences a nil validation at
	// run time. The interpreter refuses both rather than emitting a schema
	// for a tag the library cannot load.
	ErrKeysPlacement = errors.New("validate tag: keys must immediately follow a dive into a map")
	// ErrEndkeysPlacement reports an endkeys tag with no keys block open
	// and a part after it. Go-playground's parser panics on that endkeys,
	// so the interpreter refuses it too rather than emitting a schema for a
	// tag the library cannot load. A trailing endkeys with no block open is
	// not an error: the parser returns on it and the chain ends, so the
	// interpreter skips it.
	ErrEndkeysPlacement = errors.New("validate tag: endkeys closes no keys block")
	// ErrRepeatedKeyword reports two validators in one tag that both set one
	// schema keyword (email and url both name format, alpha and numeric both
	// name pattern). A schema carries one value per keyword, and
	// go-playground applies both validators, so keeping either one alone
	// would silently drop a constraint; the tag is refused instead. The
	// error names both validators.
	ErrRepeatedKeyword = errors.New("validate tag: two validators set one keyword")

	// ErrOneOfKind reports a oneof on a bool or float kind. Go-playground's
	// isOneOf formats the value as text and handles only the string and
	// integer kinds, panicking on any other, so the interpreter refuses the
	// tag rather than emitting an enum for a tag the library cannot load.
	// The refusal keys on the Go kind behind the field, so a json:",string"
	// float and a text-marshaling bool are refused too. On a bool, eq pins
	// one value; on either kind the jsonschema tag's enum= lists the values.
	ErrOneOfKind = errors.New("go-playground loads oneof only on a string or integer kind")

	// ErrStringRuleKind reports a format, pattern, or content validator on a
	// field whose instance is a string but whose Go kind is not: a
	// json:",string" numeric or bool, a non-string type that marshals itself
	// as text such as [time.Time], or a byte slice, whose instance is base64
	// text. Go-playground runs every string validator against the Go value
	// by kind, never against the text the field marshals, so number and
	// numeric accept every numeric field, the regex validators reject every
	// such field, and uri, url, and json panic on a kind they do not switch
	// on. A keyword over the marshaled text would agree with neither, so the
	// interpreter refuses the tag. The one exception is json on a byte slice,
	// which go-playground reads from the raw bytes as contentMediaType reads
	// the decoded content. The refusal keys on the Go kind, so a quoted
	// [encoding/json.Number], a string kind, keeps its string validators.
	ErrStringRuleKind = errors.New(
		"go-playground runs a string validator on a non-string kind against the Go value, not its text",
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

	return applyParts(parts, field, false, tagmodel.FormUnset)
}

// applyParts applies a sequence of validator tag parts to a field. The
// afterDive flag says whether parts starts right after a dive, and container
// is the form that dive descended into ([tagmodel.FormUnset] at the field
// level). Together they name the one place a keys block may open: the part
// right after a dive into a map.
func applyParts(parts []string, field jsonschema.FieldContext, afterDive bool, container tagmodel.Form) error {
	// The keyword-setting validators applied so far, keyed by the keyword
	// each sets, so a second validator naming the same keyword is refused
	// rather than dropped. An element level starts its own record.
	applied := map[tagmodel.Op]string{}

	for idx := 0; idx < len(parts); idx++ {
		// The whole-part matches below read the part trimmed, a widening
		// over go-playground, which refuses a padded key. The parameter is
		// cut from the untrimmed part further down and keeps its whitespace,
		// since go-playground compares against the padded literal.
		raw := parts[idx]

		part := strings.TrimSpace(raw)
		if part == "" || part == "-" {
			continue
		}

		// The parts go-playground matches whole come first, before the OR
		// split, since its parser switches on the whole comma group and only
		// the default branch splits on the pipe.
		if part == diveTag {
			// Descend into the element type. A trailing dive applies nothing
			// to the elements, a no-op as in go-playground, but the descent
			// itself still needs elements to reach: go-playground panics on a
			// dive over anything but a slice, array, or map, so a dive on a
			// field with no elements is an error here whatever follows it.
			return applyDive(parts[idx+1:], field)
		}

		// Map key validators: the parts from a keys to the first endkeys, or
		// to the end of the tag, apply to the map's keys (not modeled here)
		// and are skipped whole. Go-playground opens a block only on the
		// part right after a dive and panics elsewhere, and only its map
		// branch reads the block, so a keys anywhere else, or after a dive
		// into a slice or array, is an error rather than a schema for a tag
		// it cannot load.
		if part == keysTag {
			if !afterDive || idx != 0 {
				return ErrKeysPlacement
			}

			if container != tagmodel.FormObject {
				return fmt.Errorf("%w, and this dive descends into a %s", ErrKeysPlacement, container)
			}

			consumed, err := skipKeysBlock(parts[idx+1:])
			if err != nil {
				return err
			}

			idx += consumed

			continue
		}

		// An endkeys outside a block closes nothing. Go-playground's parser
		// panics on one unless it is the last part, where it returns and the
		// chain ends, so a trailing endkeys is a no-op here too.
		if part == endkeysTag {
			if idx != len(parts)-1 {
				return ErrEndkeysPlacement
			}

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

		alt, orGroup, err := firstAlternative(raw)
		if err != nil {
			return err
		}

		part = strings.TrimSpace(alt)

		key, value, hasValue := strings.Cut(alt, "=")

		key = strings.TrimSpace(key)
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

		// A structural key carrying a parameter or standing as an OR
		// alternative names no validator on either side: go-playground looks
		// it up as a validator there and refuses the tag. The error carries
		// the whole part, as go-playground's does.
		if isStructuralKey(key) && (hasValue || orGroup) {
			return fmt.Errorf("%w %q", ErrUnrecognizedValidator, part)
		}

		err = applyValidator(key, value, hasValue, field, applied)
		if err != nil {
			return err
		}
	}

	return nil
}

// skipKeysBlock walks the parts after a keys marker to the first endkeys,
// which closes the block, or to the end of the tag when none follows, as
// go-playground's parser collects them. The constraints inside are key
// constraints, which this dialect does not model, so nothing is applied. The
// grammar is still checked, since go-playground parses the block with the
// same rules: a keys inside it must immediately follow a dive, and a
// structural key with a parameter or an OR alternative names no validator. A
// nested block closes on the same first endkeys as the block holding it,
// since go-playground's collector stops there whatever opened before. It
// returns the number of parts consumed, the closing endkeys included.
func skipKeysBlock(parts []string) (int, error) {
	prevDive := false

	for idx := range parts {
		part := strings.TrimSpace(parts[idx])

		switch {
		case part == endkeysTag:
			return idx + 1, nil
		case part == keysTag:
			if !prevDive {
				return 0, ErrKeysPlacement
			}

		case part == "" || part == "-" || part == diveTag || isControlTag(part):
		default:
			alt, orGroup, err := firstAlternative(parts[idx])
			if err != nil {
				return 0, err
			}

			key, _, hasValue := strings.Cut(alt, "=")
			if isStructuralKey(strings.TrimSpace(key)) && (hasValue || orGroup) {
				return 0, fmt.Errorf("%w %q", ErrUnrecognizedValidator, strings.TrimSpace(alt))
			}
		}

		prevDive = part == diveTag
	}

	return len(parts), nil
}

// firstAlternative returns the first OR alternative of one comma part and
// whether the part carried a pipe at all. The | OR operator is not modeled:
// go-playground splits a comma group on the pipe and treats the alternatives
// as OR, and this dialect interprets the first alone, so a schema is never
// looser than the group. Splitting per part rather than across the whole tag
// keeps later comma-separated constraints intact, and a literal pipe in a
// parameter is written 0x7C and survives, since unescapeParam runs after
// this split. An alternative with no key, anywhere in the group, is a tag
// go-playground refuses outright: its parser splits every alternative and
// panics on an empty key in any of them. So a trailing pipe or a doubled one
// is an error here rather than a group whose tail is dropped in silence.
func firstAlternative(raw string) (string, bool, error) {
	first, _, orGroup := strings.Cut(raw, "|")
	if !orGroup {
		return raw, false, nil
	}

	for alt := range strings.SplitSeq(raw, "|") {
		key, _, _ := strings.Cut(alt, "=")
		if strings.TrimSpace(key) == "" {
			return "", true, fmt.Errorf("validate tag: empty OR alternative in %q", strings.TrimSpace(raw))
		}
	}

	return first, true, nil
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
//
// Applied records, per keyword, the validator in this tag that set it. A
// validator whose operation replaces the keyword's value rather than
// composing with it (the string keywords and the divisor) is refused with
// [ErrRepeatedKeyword] when one already set that keyword, since the model
// would keep the first and drop the second without a word.
func applyValidator(
	key, value string, hasValue bool, field jsonschema.FieldContext, applied map[tagmodel.Op]string,
) error {
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

	// A string validator on a numeric or bool kind reads the Go value in
	// go-playground, so the string schema a coerced field takes is not the
	// value it judges. The model applies the keyword to any string instance,
	// which is right for a dialect naming the keyword outright, so the
	// kind check lives here with the other go-playground kind rules. The
	// native number and bool forms need no check: the model rejects a
	// string keyword there for want of a string.
	if bound.Op.IsStringKeyword() && readsGoValueNotText(shape, bound.Op) {
		return fmt.Errorf("validate tag: %s: %w: %s", key, ErrStringRuleKind, shape.Elem)
	}

	if bound.Op.Overwrites() {
		if prev, ok := applied[bound.Op]; ok {
			return fmt.Errorf("%w: %s and %s", ErrRepeatedKeyword, prev, key)
		}

		applied[bound.Op] = key
	}

	if bound.Op == tagmodel.OpOneOf {
		err = checkOneOf(field, shape, bound.Params.Values())
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

// isCoercedForm reports whether a form is a numeric or bool value whose
// instance is a string: a json:",string" field or a type marshaling itself
// as text.
func isCoercedForm(form tagmodel.Form) bool {
	return form == tagmodel.FormCoercedNumber || form == tagmodel.FormCoercedBool
}

// readsGoKindAsText reports whether go-playground judges a string validator on
// the kind without the text the field marshals: every integer, float, and
// bool kind. Its isNumber and isNumeric return true on a numeric kind before
// reading anything, and the other string validators run their regex over
// reflect's description of a non-string value, which no marshaled text
// equals.
func readsGoKindAsText(kind reflect.Kind) bool {
	return numkind.IsInteger(kind) || numkind.IsFloat(kind) || kind == reflect.Bool
}

// readsGoValueNotText reports whether go-playground judges the string
// validator op on a field of this shape against the Go value rather than the
// text the field marshals: a coerced numeric or bool kind, a non-string kind
// that marshals itself as text, or a byte slice, whose base64 text no
// validator there reads. The json validator is the one exception on a byte
// slice, since go-playground reads the raw bytes there as contentMediaType
// reads the decoded content. A string kind that marshals itself as text
// keeps its validators: go-playground reads the Go string, which is the
// text in every case the rig has found.
func readsGoValueNotText(shape tagmodel.Shape, op tagmodel.Op) bool {
	switch shape.Form {
	case tagmodel.FormByteString:
		return op != tagmodel.OpContentMediaType
	case tagmodel.FormTextString:
		return shape.Kind != reflect.String
	default:
		return isCoercedForm(shape.Form) && readsGoKindAsText(shape.Kind)
	}
}

// checkOneOf rejects a oneof go-playground could never run or match on the
// value the rule reaches. A bool or float kind is refused outright, since
// go-playground panics on it; the check reads the Go kind, not the JSON form,
// so a json:",string" float and a text-marshaling bool are refused alike. On
// a number every token must be the canonical spelling. On a sequence the rule
// retargets onto the elements, so the check descends through the element
// contexts the same way and runs on every leaf; a oneof written on a []int
// and one written under a dive then refuse the same tokens, and a []float64
// refuses under both spellings. The spelling check reads the Go kind, not
// the JSON form: go-playground formats an integer with strconv and compares
// the text whatever the json tag says, so a coerced integer refuses -0 as a
// native one does, where canonicalizing it against the serialized text would
// enumerate a "0" that comparison never matches.
func checkOneOf(field jsonschema.FieldContext, shape tagmodel.Shape, tokens []string) error {
	if shape.Elem != nil && (numkind.IsFloat(shape.Kind) || shape.Kind == reflect.Bool) {
		return fmt.Errorf("%w: %s", ErrOneOfKind, shape.Elem)
	}

	if shape.Form == tagmodel.FormNumber || shape.Form == tagmodel.FormCoercedNumber {
		// An encoding/json.Number is the one string kind with a number form,
		// bare or quoted. Go-playground compares its text against the raw
		// tokens, and encoding/json writes only literals inside the JSON
		// grammar, so a token outside it can equal no marshaled value.
		if shape.Kind == reflect.String {
			return checkJSONNumberOneOf(tokens)
		}

		return checkCanonicalOneOfKind(shape.Kind, tokens)
	}

	if shape.Form != tagmodel.FormArray {
		return nil
	}

	elems := field.ElementContexts()
	for i := range elems {
		err := checkOneOf(elems[i], shapeOf(elems[i]), tokens)
		if err != nil {
			return err
		}
	}

	return nil
}

// checkCanonicalOneOfKind rejects a oneof token on an integer kind whose
// spelling go-playground could never match. Its isOneOf compares the field's
// value formatted with strconv against the raw tokens, so a token such as +1
// or 01 matches no value at all, while the enum this dialect emits would
// admit the number it parses to. A token that does not parse at the kind's
// width is left to the model, which reports the parse or range fault. A
// float kind never reaches here, since checkOneOf refuses it first.
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

		default:
			return nil
		}

		if canonical != tok {
			return fmt.Errorf("%q is not the canonical spelling %q go-playground compares against", tok, canonical)
		}
	}

	return nil
}

// checkJSONNumberOneOf rejects a oneof token on an [encoding/json.Number]
// field that the JSON number grammar refuses. Such a literal never marshals,
// so the enum this dialect would emit admits a number no value spells.
func checkJSONNumberOneOf(tokens []string) error {
	for _, tok := range tokens {
		if !jsonvalue.IsJSONNumber(tok) {
			return fmt.Errorf("%q is not a JSON number, so no marshaled value equals it", tok)
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
