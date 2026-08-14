package tagmodel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// boolTrue and boolFalse are the only boolean literals either tag grammar
// spells.
const (
	boolTrue  = "true"
	boolFalse = "false"
)

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
// canonicalizes the spellings go-playground accepts but encoding/json never
// emits, so "5.0", "+5", and "1e2" all become the text the instance carries.
func (sh Shape) ParseScalar(lit string, pol Policy) (any, error) {
	if pol.AllowNullScalar && lit == typename.Null {
		if !sh.Nullable {
			return nil, fmt.Errorf("cannot assign null to non-nullable type %s", sh.Kind)
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

	case FormCoercedNumber, FormCoercedBool:
		return sh.coercedText(lit)

	case FormCoercedString:
		return sh.quotedText(lit)

	default:
		return nil, fmt.Errorf("cannot assign scalar value %q to type %s", lit, sh.Kind)
	}
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
		n, err := strconv.ParseUint(lit, 10, numkind.UintBitSize(sh.Kind))
		if err != nil {
			return nil, fmt.Errorf("invalid unsigned integer %q: %w", lit, err)
		}

		return n, nil

	case numkind.IsInteger(sh.Kind):
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
// policy, plus the range check the field's own kind implies.
//
// The value is stored at 64 bits even for a float32 field, so it is the float64
// nearest the decimal the author wrote rather than its float32-rounded form:
// rounding 0.1 to float32 stores 0.10000000149011612, which a {"v":0.1} instance
// could never match against its own const. The 32-bit reparse is therefore an
// overflow check and nothing else.
func parseFloatLiteral(lit string, kind reflect.Kind) (any, error) {
	n, err := constraint.ParseDecimalFloat(lit)
	if err != nil {
		//nolint:wrapcheck // The shared policy owns the spelling and its message.
		return nil, err
	}

	if kind == reflect.Float32 {
		_, err = strconv.ParseFloat(lit, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", lit, err)
		}
	}

	return n, nil
}

// coercedText parses a literal at the shape's real kind, then converts it back
// to the shape's Go type and marshals it, yielding the text the field's own
// value serializes to. Parsing at the real kind keeps the range check; the
// round-trip is what lets a string-marshaling type contribute its own form. A
// quoted result is unquoted to the content the string schema compares against.
func (sh Shape) coercedText(lit string) (any, error) {
	var (
		parsed any
		err    error
	)

	if sh.Form == FormCoercedBool {
		parsed, err = ParseBoolLiteral(lit)
	} else {
		parsed, err = sh.parseNumber(lit)
	}

	if err != nil {
		return nil, err
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

// quotedText serializes a string literal the way a json:",string" string field
// emits it: encoding/json encodes the already-encoded string a second time, so
// the instance is the JSON-quoted text, quotes and escapes included (value abc
// marshals as the JSON string "\"abc\""). The literal converts through the
// shape's own Go type first, mirroring coercedText, and unlike there the
// quotes are the point and are kept.
func (sh Shape) quotedText(lit string) (any, error) {
	out, err := json.Marshal(reflect.ValueOf(lit).Convert(sh.Elem).Interface())
	if err != nil {
		return nil, fmt.Errorf("cannot serialize %q as %s: %w", lit, sh.Elem, err)
	}

	return string(out), nil
}

// zeroLiteral is the literal whose serialized form the non-zero assertion
// forbids on a coerced shape: the value the field emits when it holds its Go
// zero. It routes through the same [Shape.ParseScalar] every other coerced
// operation uses, so a string-marshaling type forbids the text it actually
// writes rather than a hardcoded "0" it never emits.
func (sh Shape) zeroLiteral() string {
	switch sh.Form {
	case FormCoercedBool:
		return boolFalse
	case FormCoercedString:
		return "" // The empty string, whose quoted serialization is `""`.
	default:
		return "0"
	}
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
