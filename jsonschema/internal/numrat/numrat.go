// Package numrat is the exact-decimal arithmetic core for JSON numbers. It
// decomposes a decimal literal into a canonical 0.sig x 10^exp form in O(len)
// without expanding exponents, so it is safe on adversarial input, and expands
// that form into an exact [big.Rat] only within bounds where the expansion is
// provably cheap. Numbers outside those bounds are compared by magnitude class
// and truncated significand instead, and integral multipleOf is decided through
// modular arithmetic so an over-cap magnitude is never materialized. The
// reflection generator and the validator share these conversions, so the
// numeric reasoning lives in one place.
package numrat

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strconv"
)

// MaxNumberLen bounds the number of significant digits and the decimal
// exponent magnitude that the validator expands into an exact [big.Rat].
// [big.Rat.SetString] is quadratic in the digit count and materializes
// exponents as full integers (a 9-character literal like 1e1000000 expands to
// a million-digit number), so an adversarial literal can cost seconds of CPU
// and large allocations. A number outside these bounds can never equal a
// schema bound or const: a float64's exact decimal expansion has at most ~767
// significant digits and a decimal exponent within about ±324, far inside the
// cap. Such numbers are compared by magnitude class and truncated significand
// instead of being expanded (see validateNumericUnbounded).
const MaxNumberLen = 4096

// decExpClamp caps the parsed decimal exponent so arithmetic on it cannot
// overflow. Every magnitude beyond MaxNumberLen behaves identically (the value
// is outside the float64 range either way), so clamping does not change any
// comparison.
const decExpClamp = 1 << 30

// DecNumber is the canonical decomposition of a decimal number literal:
// value = ±0.sig × 10^exp, where sig holds the significant digits with leading
// and trailing zeros stripped. Zero has an empty sig (its exp and neg carry no
// meaning). The decomposition is computed in O(len) without expanding
// exponents, so it is safe on adversarial input, and it is unique: two
// literals denote the same value exactly when their nonzero decompositions
// match.
type DecNumber struct {
	sig string
	exp int
	neg bool
}

// Sig returns the significant digits of the decomposition, with leading and
// trailing zeros stripped. A zero value has an empty significand.
func (d DecNumber) Sig() string { return d.sig }

// Exp returns the base-10 exponent of the canonical 0.sig × 10^exp form. It is
// clamped for an over-cap magnitude, so it is meaningful only relative to the
// significand length, not as an exact magnitude.
func (d DecNumber) Exp() int { return d.exp }

// Neg reports whether the value is negative.
func (d DecNumber) Neg() bool { return d.neg }

// ParseDecNumber decomposes a decimal literal (the JSON number grammar, with a
// leading '+' and bare ".5"/"5." forms also accepted for parity with
// [big.Rat.SetString]) into canonical DecNumber form. It reports false for
// anything else, including the fraction and hexadecimal forms [big.Rat]
// accepts, which JSON cannot produce.
func ParseDecNumber(s string) (DecNumber, bool) {
	var d DecNumber

	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		d.neg = s[i] == '-'
		i++
	}

	intStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}

	intDigits := s[intStart:i]

	var fracDigits string

	if i < len(s) && s[i] == '.' {
		i++

		fracStart := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}

		fracDigits = s[fracStart:i]
	}

	if intDigits == "" && fracDigits == "" {
		return DecNumber{}, false
	}

	var exp int64

	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++

		expNeg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			expNeg = s[i] == '-'
			i++
		}

		expStart := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			// Saturate instead of overflowing; precision past the clamp cannot
			// change any comparison.
			if exp < decExpClamp {
				exp = exp*10 + int64(s[i]-'0')
			}

			i++
		}

		if i == expStart {
			return DecNumber{}, false
		}

		if expNeg {
			exp = -exp
		}
	}

	if i != len(s) {
		return DecNumber{}, false
	}

	// DigitAt addresses the combined integer+fraction digit string without
	// concatenating it.
	digitsLen := len(intDigits) + len(fracDigits)
	digitAt := func(i int) byte {
		if i < len(intDigits) {
			return intDigits[i]
		}

		return fracDigits[i-len(intDigits)]
	}

	lead := 0
	for lead < digitsLen && digitAt(lead) == '0' {
		lead++
	}

	if lead == digitsLen {
		// All digits are zero: canonical zero. The sign is discarded so 0, -0,
		// and 0e5 share a single form, matching big.Rat equality.
		return DecNumber{}, true
	}

	trail := 0
	for digitAt(digitsLen-1-trail) == '0' {
		trail++
	}

	// The significand spans the combined digits from lead to digitsLen-trail;
	// slice it out of whichever part holds it, concatenating only when it
	// straddles the decimal point.
	start, end := lead, digitsLen-trail
	switch {
	case end <= len(intDigits):
		d.sig = intDigits[start:end]
	case start >= len(intDigits):
		d.sig = fracDigits[start-len(intDigits) : end-len(intDigits)]
	default:
		d.sig = intDigits[start:] + fracDigits[:end-len(intDigits)]
	}

	// Value = sig × 10^(exp - len(frac) + trail), and as 0.sig form that shifts
	// by len(sig) more.
	e := int64(len(d.sig)) + exp - int64(len(fracDigits)) + int64(trail)
	switch {
	case e > decExpClamp:
		e = decExpClamp
	case e < -decExpClamp:
		e = -decExpClamp
	}

	d.exp = int(e)

	return d, true
}

// DecCanonicalExp returns the exact base-10 exponent of s in its canonical
// 0.sig x 10^exp form, as an unclamped [big.Int]: parsedExp + len(intDigits) -
// lead, where lead is the count of leading zeros across the integer and fraction
// digits. The exponent ParseDecNumber stores is clamped so arithmetic on it
// stays bounded, which is correct when comparing a huge number against an
// in-range value but collapses two distinct huge magnitudes onto one DecNumber.
// This exact form is used on the rare path where two such literals share a
// clamped DecNumber, so distinct values stay distinct. The argument s must
// already be a valid decimal literal (ParseDecNumber returned true).
func DecCanonicalExp(s string) *big.Int {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}

	intStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}

	intLen := i - intStart

	lead := 0
	for j := intStart; j < i && s[j] == '0'; j++ {
		lead++
	}

	if i < len(s) && s[i] == '.' {
		i++

		// All integer digits were zero, so leading zeros continue into the
		// fraction (e.g. 0.05 has two leading zeros across "005").
		if lead == intLen {
			for i < len(s) && s[i] == '0' {
				lead++
				i++
			}
		}

		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}

	exp := new(big.Int)
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++

		neg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			neg = s[i] == '-'
			i++
		}

		exp.SetString(s[i:], 10)

		if neg {
			exp.Neg(exp)
		}
	}

	return exp.Add(exp, big.NewInt(int64(intLen-lead)))
}

// IsIntegral reports whether the value is a mathematical integer: zero, or a
// significand that sits entirely left of the decimal point.
func (d DecNumber) IsIntegral() bool {
	return d.sig == "" || d.exp >= len(d.sig)
}

// ExactlyComparable reports whether the value can be expanded into a [big.Rat]
// at bounded cost: at most MaxNumberLen significant digits scaled by at most
// MaxNumberLen decimal places. Values outside these bounds are compared by
// magnitude class instead (see validateNumericUnbounded) and can never equal a
// float64 or integer (see equalGuarded).
func (d DecNumber) ExactlyComparable() bool {
	return len(d.sig) <= MaxNumberLen && d.exp <= MaxNumberLen && d.exp >= -MaxNumberLen
}

// Rat expands the canonical form into an exact rational. The cost is bounded
// only for ExactlyComparable values; callers must check that first.
func (d DecNumber) Rat() *big.Rat {
	if d.sig == "" {
		return new(big.Rat)
	}

	num := new(big.Int)
	num.SetString(d.sig, 10) // sig is all digits, so this cannot fail

	shift := int64(d.exp) - int64(len(d.sig))

	absShift := shift
	if absShift < 0 {
		absShift = -absShift
	}

	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(absShift), nil)

	r := new(big.Rat)
	if shift >= 0 {
		r.SetInt(num.Mul(num, pow))
	} else {
		r.SetFrac(num, pow)
	}

	if d.neg {
		r.Neg(r)
	}

	return r
}

// CmpRat orders a value that is not ExactlyComparable against an exact
// rational derived from a float64 bound, returning -1 (below) or +1 (above).
// Exact equality cannot occur, because every float64 expands to at most ~767
// significant decimal digits within exponent ±324, inside the caps. So 0 is
// never returned and inclusive/exclusive bounds behave identically.
func (d DecNumber) CmpRat(b *big.Rat) int {
	sign := 1
	if d.neg {
		sign = -1
	}

	// Huge magnitude: |value| ≥ 10^MaxNumberLen exceeds every finite float64,
	// so the sign alone decides.
	if d.exp > MaxNumberLen {
		return sign
	}

	// Tiny magnitude: 0 < |value| < 10^-MaxNumberLen sits strictly between
	// zero and the smallest nonzero float64, so it compares as an epsilon of
	// its sign: above every bound on or below zero, below every bound above
	// zero (and mirrored when negative).
	if d.exp < -MaxNumberLen {
		if d.neg {
			if b.Sign() < 0 {
				return 1
			}

			return -1
		}

		if b.Sign() > 0 {
			return -1
		}

		return 1
	}

	// Over-precise: more significant digits than any float64 expansion.
	// Truncating the significand moves the magnitude strictly toward zero (the
	// dropped tail is nonzero since sig carries no trailing zeros), and no
	// float64 fits strictly between the truncated and full values (that would
	// take more than MaxNumberLen significant digits). The truncated ordering
	// therefore decides, with ties broken away from zero.
	//
	// The only inputs that reach this line have len(sig) > MaxNumberLen (the
	// huge/tiny branches above caught every out-of-range exponent, so the
	// remaining non-ExactlyComparable reason is excess precision). Clamp the cut
	// anyway so a future caller passing a shorter significand degrades to an
	// exact compare instead of a slice-bounds panic.
	t := DecNumber{sig: d.sig[:min(MaxNumberLen, len(d.sig))], exp: d.exp, neg: d.neg}
	if c := t.Rat().Cmp(b); c != 0 {
		return c
	}

	return sign
}

// JSONNumberIsIntegral reports whether a [json.Number] denotes a mathematical
// integer (e.g. "1.0", "1e3", or a value far beyond the int64 range). The
// canonical decomposition answers exactly in O(n) at any magnitude or
// precision, without the quadratic [big.Rat] parse a long or large-exponent
// literal would otherwise incur.
func JSONNumberIsIntegral(n json.Number) bool {
	d, ok := ParseDecNumber(string(n))

	return ok && d.IsIntegral()
}

// IsIntegralInstance reports whether a numeric instance (a [json.Number] or a
// float64) has an integer value. A [json.Number] parses its decimal exactly via
// [big.Rat], so integrality holds at any magnitude; a float64 uses Trunc to avoid
// the int64() saturation that misclassifies large integral floats (e.g. 1e30).
// It is the single definition shared by instanceType and instanceMatchesType.
func IsIntegralInstance(v any) bool {
	switch val := v.(type) {
	case json.Number:
		_, err := val.Int64()
		if err == nil {
			return true
		}

		return JSONNumberIsIntegral(val)

	case float64:
		return !math.IsInf(val, 0) && val == math.Trunc(val)
	}

	return false
}

// SchemaNumberRat converts a schema-authored numeric value to an exact
// rational. A float64 expands through its shortest decimal ([Float64ToRat]) so
// that, e.g., schema 0.1 compares as 1/10 rather than its binary expansion,
// matching the numeric-bound keywords; integer kinds convert exactly. A
// non-numeric value, or a non-finite float, reports false so the caller treats
// the schema value as a non-number.
func SchemaNumberRat(v any) (*big.Rat, bool) {
	if f, ok := v.(float64); ok {
		r := Float64ToRat(f)
		if r == nil {
			return nil, false
		}

		return r, true
	}

	if n, ok := v.(json.Number); ok {
		// A hand-built const/enum may carry a json.Number rather than the
		// float64 the schema parser yields. Expand it the way ToBigRat expands
		// instance numbers so both take the shortest-decimal rational path
		// rather than upstream's exact-binary float semantics.
		d, parsed := ParseDecNumber(string(n))
		if !parsed || !d.ExactlyComparable() {
			return nil, false
		}

		return d.Rat(), true
	}

	rv := reflect.ValueOf(v)
	switch {
	case !rv.IsValid():
		return nil, false
	case rv.CanInt():
		return new(big.Rat).SetInt64(rv.Int()), true
	case rv.CanUint():
		return new(big.Rat).SetUint64(rv.Uint()), true
	}

	return nil, false
}

// EnumMemberRats returns the rational forms of an enum's numeric members,
// aligned by index with enum (nil for a non-numeric member). It returns nil
// when no member is numeric, so [precomputeSchema] stores an entry only for an
// enum that can take the fast numeric-comparison path.
func EnumMemberRats(enum []any) []*big.Rat {
	var rats []*big.Rat

	for i, member := range enum {
		r, ok := SchemaNumberRat(member)
		if !ok {
			continue
		}

		if rats == nil {
			rats = make([]*big.Rat, len(enum))
		}

		rats[i] = r
	}

	return rats
}

// NumericRat converts the numeric Go kinds [jsonschema.Equal] recognizes
// (other than [json.Number]) to an exact rational.
func NumericRat(v any) (*big.Rat, bool) {
	rv := reflect.ValueOf(v)
	r := new(big.Rat)

	switch {
	case !rv.IsValid():
		return nil, false
	case rv.CanInt():
		r.SetInt64(rv.Int())
	case rv.CanUint():
		r.SetUint64(rv.Uint())
	case rv.CanFloat():
		f := rv.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, false
		}

		r.SetFloat64(f)

	default:
		return nil, false
	}

	return r, true
}

// ToBigRat converts a numeric value to [*big.Rat] for precise comparison.
func ToBigRat(v any) (*big.Rat, bool) {
	switch val := v.(type) {
	case float64:
		// Use the shortest decimal representation so that, e.g., float64(1.01)
		// compares as 101/100 rather than its exact binary expansion. This
		// matches how schema bound values are converted (Float64ToRat). A
		// non-finite value yields nil, which is reported as not-a-number so
		// numeric keywords skip it rather than dereferencing a nil *big.Rat.
		r := Float64ToRat(val)
		if r == nil {
			return nil, false
		}

		return r, true

	case json.Number:
		// DoS guard: decompose canonically (O(n), no exponent expansion) and
		// expand into a rational only when that is provably cheap. Anything
		// else is reported unparseable so validateNumeric falls back to the
		// magnitude-class comparison.
		d, ok := ParseDecNumber(string(val))
		if !ok || !d.ExactlyComparable() {
			return nil, false
		}

		return d.Rat(), true
	}

	return nil, false
}

// IsNumeric reports whether a value is a numeric type
// (float64 or [json.Number]).
func IsNumeric(v any) bool {
	switch v.(type) {
	case float64, json.Number:
		return true
	}

	return false
}

// Float64ToRat converts a float64 to a [big.Rat] using its shortest decimal
// representation to avoid precision artifacts (e.g. float64(1.1) becoming
// 1.100000000000000088... When using [big.Rat.SetFloat64]).
func Float64ToRat(f float64) *big.Rat {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		// Non-finite values have no rational form. Callers treat a nil result as
		// "not a JSON number" (JSON cannot represent Inf or NaN).
		return nil
	}

	// A finite float64 always formats to a decimal string that SetString
	// parses, so the parse cannot fail here; a nil result would only arise from
	// the non-finite guard above, which callers already treat as "not a number".
	s := strconv.FormatFloat(f, 'f', -1, 64)

	r := new(big.Rat)
	_, _ = r.SetString(s)

	return r
}

// IntegerMultipleOf reports whether the integral value of literal (decomposed
// as d) is an exact multiple of the positive rational divisor m, without
// expanding an over-cap magnitude. Writing the value as sig*10^k with k >= 0 and
// m as the reduced p/q, the value is a multiple of m exactly when p divides it,
// since gcd(p, q) = 1 means q contributes no factor of p. The check is then
// (sig mod p)*(10^k mod p) mod p == 0, with the power reduced modulo p so 10^k
// is never materialized. The exponent comes from the literal because
// DecNumber.exp is clamped for an over-cap magnitude. The caller must pass an
// integral d (see [DecNumber.IsIntegral]) and a positive m.
func IntegerMultipleOf(d DecNumber, literal string, m *big.Rat) bool {
	p := new(big.Int).Abs(m.Num())
	if p.Sign() == 0 || d.sig == "" {
		return true // No real divisor, or a zero value: a multiple either way.
	}

	sig, ok := new(big.Int).SetString(d.sig, 10)
	if !ok {
		return true // sig is all digits by construction, so this cannot fail.
	}

	k := new(big.Int).Sub(DecCanonicalExp(literal), big.NewInt(int64(len(d.sig))))
	if k.Sign() < 0 {
		return true // Not integral after all; the caller should have screened it.
	}

	pow := new(big.Int).Exp(big.NewInt(10), k, p)
	rem := new(big.Int).Mod(sig, p)
	rem.Mul(rem, pow)
	rem.Mod(rem, p)

	return rem.Sign() == 0
}
