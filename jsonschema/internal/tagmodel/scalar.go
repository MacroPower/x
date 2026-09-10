package tagmodel

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// boolTrue and boolFalse are the only boolean literals either tag grammar
// spells.
const (
	boolTrue  = "true"
	boolFalse = "false"
)

// ErrNullNotAdmitted reports a null literal on an occurrence that admits no
// null. A front-end wraps the sentinel with its own dialect prefix and names
// the type the literal had nothing to assign to. A front-end that refuses the
// literal before calling [Shape.ParseScalar] returns the same sentinel, so one
// identity covers both sites.
var ErrNullNotAdmitted = errors.New("cannot assign null to non-nullable type")

// ParseScalar turns one tag literal into the value a const, enum member,
// default, or forbidden value carries. It dispatches on [Shape.Form], so a
// coerced shape can never receive a native value and a native one can never
// receive a serialized string: the mistake is not available to make.
//
// A native scalar parses at the shape's real Go kind and is returned as the
// widest type that holds it exactly (int64, uint64, float64, bool, string), so
// a literal the field could never hold -- 200 on an int8 -- overflows here
// rather than pinning the schema to a constant no instance satisfies.
//
// A coerced scalar parses the same way and is then converted back to the
// shape's own Go type and marshaled, so the result is the text the field
// actually emits. That round-trip is what makes a string-marshaling type
// contribute its own serialized form: an int with json:",string" yields "5",
// while a type whose MarshalText writes "L5" yields exactly that. It also
// canonicalizes the float spellings go-playground accepts but encoding/json
// never emits, so "5.0" and "1e2" both become the text the instance carries.
// An integer-kind literal takes the JSON integer grammar
// ([constraint.CheckIntegerLiteral]) on both paths, so there is nothing of
// its spelling left to canonicalize.
func (sh Shape) ParseScalar(lit string, pol Policy) (any, error) {
	if pol.AllowNullScalar && lit == typename.Null {
		if !sh.Nullable {
			return nil, fmt.Errorf("%w %s", ErrNullNotAdmitted, sh.Kind)
		}

		return nil, nil //nolint:nilnil // Intentional: nil represents JSON null.
	}

	switch sh.Form {
	case FormString, FormTextString:
		return lit, nil

	case FormBool:
		return ParseBoolLiteral(lit)

	case FormNumber:
		return sh.parseNumber(lit)

	case FormCoercedNumber, FormCoercedBool, FormCoercedString:
		return sh.coercedText(lit, false)

	case FormByteString:
		return sh.byteText(lit)

	default:
		return nil, fmt.Errorf("cannot assign scalar value %q to type %s", lit, sh.Kind)
	}
}

// byteText reads a literal for a byte slice or array, whose instance is the
// base64 text of its bytes. The literal is that text, checked to be one the
// encoder produces (standard alphabet, padded) so a value no instance could
// carry is an error rather than a pin nothing satisfies, and on a byte array
// checked to decode to the array's length, as the instance always does.
func (sh Shape) byteText(lit string) (any, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(lit)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 %q: %w", lit, err)
	}

	if sh.Kind == reflect.Array && len(decoded) != sh.Elem.Len() {
		return nil, fmt.Errorf("base64 %q decodes to %d bytes, not the %d of %s",
			lit, len(decoded), sh.Elem.Len(), sh.Elem)
	}

	return lit, nil
}

// ParseScalars parses each literal of an enumeration, preserving tag order.
func (sh Shape) ParseScalars(lits []string, pol Policy) ([]any, error) {
	out := make([]any, len(lits))

	for i, lit := range lits {
		v, err := sh.ParseScalar(lit, pol)
		if err != nil {
			return nil, err
		}

		out[i] = v
	}

	return out, nil
}

// parseNumber parses a numeric literal at the shape's kind width. Unsigned is
// tested first because [numkind.IsInteger] reports true for it too.
func (sh Shape) parseNumber(lit string) (any, error) {
	switch {
	case numkind.IsUnsigned(sh.Kind):
		// Return uint64: neither int nor float64 holds every uint64 exactly.
		n, err := constraint.ParseUnsignedLiteral(lit, numkind.UintBitSize(sh.Kind))
		if err != nil {
			//nolint:wrapcheck // The shared policy owns the spelling and its message.
			return nil, err
		}

		return n, nil

	case numkind.IsInteger(sh.Kind):
		err := constraint.CheckIntegerLiteral(lit)
		if err != nil {
			//nolint:wrapcheck // The shared policy owns the spelling and its message.
			return nil, err
		}

		// Return int64, not a platform int, so a value above 2^31-1 survives on
		// a 32-bit build.
		n, err := strconv.ParseInt(lit, 10, numkind.IntBitSize(sh.Kind))
		if err != nil {
			return nil, fmt.Errorf("invalid integer %q: %w", lit, err)
		}

		return n, nil
	}

	return parseFloatLiteral(lit, sh.Kind)
}

// parseFloatLiteral parses a float field value: the shared decimal spelling
// policy, plus the width check the field's own kind implies.
//
// The value is stored at 64 bits even for a float32 field, so it is the float64
// nearest the decimal the author wrote rather than its float32-rounded form:
// rounding 0.1 to float32 would store 0.10000000149011612, which a {"v":0.1}
// instance could never match against its own const. The width check instead
// requires the literal to be one a float32 marshals as, so 0.1 passes and
// 10.0000001, which every float32 near it renders as 10, is an error.
func parseFloatLiteral(lit string, kind reflect.Kind) (any, error) {
	n, err := constraint.ParseDecimalFloat(lit)
	if err != nil {
		//nolint:wrapcheck // The shared policy owns the spelling and its message.
		return nil, err
	}

	err = constraint.CheckFloatLiteral(lit, kind)
	if err != nil {
		//nolint:wrapcheck // The shared policy owns the width and its message.
		return nil, err
	}

	return n, nil
}

// coercedText parses a literal at the shape's real kind, then converts it back
// to the shape's Go type and marshals it, yielding the text the field's own
// value serializes to. Parsing at the real kind keeps the range check; the
// round-trip is what lets a string-marshaling type contribute its own form. A
// quoted result is unquoted to the content the string schema compares against.
//
// A float literal spelled -0 is the value 0, and the text is the canonical
// "0" unless keepSign is set: Go compares the two zeros as one, so
// go-playground's eq=-0 accepts the same values eq=0 does, and a pin on the
// text "-0", which encoding/json writes for the sign bit alone, would reject
// the zero the field ordinarily emits. The forbid side sets keepSign to name
// both texts, see [Shape.zeroLiterals].
func (sh Shape) coercedText(lit string, keepSign bool) (any, error) {
	var (
		parsed any
		err    error
	)

	switch {
	case sh.Form == FormCoercedBool:
		parsed, err = ParseBoolLiteral(lit)

	case sh.Form == FormCoercedString, reflectkind.IsJSONNumber(sh.Elem):
		// A string kind has no number to parse: the literal is the Go value,
		// and the marshal below is what turns it into the text the field
		// emits. A json.Number is the same case, since it holds its literal
		// as text and has no width to parse at, and the marshal is what
		// validates it. Parsing a number first would be both pointless and
		// unconvertible, since reflect refuses a float64 to a string-kinded
		// type.
		parsed = lit

	default:
		parsed, err = sh.parseNumber(lit)
	}

	if err != nil {
		return nil, err
	}

	if f, isFloat := parsed.(float64); isFloat && f == 0 && !keepSign {
		parsed = float64(0)
	}

	out, err := json.Marshal(reflect.ValueOf(parsed).Convert(sh.Elem).Interface())
	if err != nil {
		return nil, fmt.Errorf("cannot serialize %q as %s: %w", lit, sh.Elem, err)
	}

	if len(out) > 0 && out[0] == '"' {
		var unquoted string

		err = json.Unmarshal(out, &unquoted)
		if err != nil {
			return nil, fmt.Errorf("cannot serialize %q as %s: %w", lit, sh.Elem, err)
		}

		return unquoted, nil
	}

	return string(out), nil
}

// zeroLiterals are the literals whose serialized forms the non-zero assertion
// forbids on a coerced shape, canonical spelling first. Each is a text the field
// emits when it holds a Go zero. They route through the same
// [Shape.ParseScalar] every other coerced operation uses, so a string-marshaling
// type forbids the text it actually writes rather than a hardcoded "0" it never
// emits.
//
// A coerced float has two of them. Go's negative zero compares equal to zero, so
// go-playground rejects it wherever it rejects zero. Because encoding/json
// writes the sign bit, an instance can carry "-0", and a forbid side naming only
// "0" would admit a value the reference validator rejects. An
// [encoding/json.Number] classifies as a coerced number with a string kind and
// holds the text "-0" as an ordinary value, so the float gate reads the Go kind
// rather than asking whether the shape is an integer.
//
// A coerced string's zero is the empty Go string, and what it forbids is
// whatever text that empty string marshals to.
//
// The texts come from [Shape.zeroTexts], which keeps the sign of the float
// pair where [Shape.ParseScalar] folds it into the canonical "0".
func (sh Shape) zeroLiterals() []string {
	switch {
	case sh.Form == FormCoercedBool:
		return []string{boolFalse}
	case sh.Form == FormCoercedString:
		return []string{""}
	case sh.Form == FormCoercedNumber && numkind.IsFloat(sh.Kind):
		return []string{"0", "-0"}
	default:
		return []string{"0"}
	}
}

// zeroTexts parses each zero literal to the text the field emits for it, the
// sign of a negative zero kept, so the forbid side names every text a Go zero
// serializes to.
func (sh Shape) zeroTexts() ([]any, error) {
	spellings := sh.zeroLiterals()
	out := make([]any, len(spellings))

	for i, lit := range spellings {
		v, err := sh.coercedText(lit, true)
		if err != nil {
			return nil, err
		}

		out[i] = v
	}

	return out, nil
}

// ParseBoolLiteral parses the two boolean literals both tag grammars spell,
// deliberately not [strconv.ParseBool]'s wider "1"/"t"/"TRUE" set, which neither
// grammar accepts. It is exported so a dialect's own boolean keys read the same
// way as the ones that reach it through [Bind].
func ParseBoolLiteral(lit string) (bool, error) {
	switch lit {
	case boolTrue:
		return true, nil
	case boolFalse:
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", lit)
	}
}
