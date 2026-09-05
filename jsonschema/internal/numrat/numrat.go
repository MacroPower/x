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
	"math"
	"math/big"
	"strconv"
	"strings"
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

// DecCanonicalExpEqual reports whether two decimal literals have the same
// exact canonical exponent (the value [DecCanonicalExp] returns), at cost
// linear in the literals. DecCanonicalExp materializes the exponent digit run
// with [big.Int.SetString], which is quadratic in the run length, so deciding
// equality through it hands an adversarial large-exponent literal exactly the
// expansion MaxNumberLen exists to prevent; this comparison never builds a
// [big.Int] from an unbounded run. Both arguments must already be valid
// decimal literals (ParseDecNumber returned true).
func DecCanonicalExpEqual(a, b string) bool {
	da, na, ka := decExpParts(a)
	db, nb, kb := decExpParts(b)

	// Small runs expand at bounded cost; reuse the exact big.Int form.
	const smallRun = 32

	if len(da) <= smallRun && len(db) <= smallRun {
		return DecCanonicalExp(a).Cmp(DecCanonicalExp(b)) == 0
	}

	// At least one run holds more than 32 digits, so that literal exponent
	// magnitude is at least 10^32, while each offset is bounded by its
	// literal's length (far below 10^18 for any literal that fits in
	// memory). The canonical exponents Pa+ka and Pb+kb can then be equal
	// only when the literal exponents nearly cancel: same sign, lengths
	// within one, and a digit-string difference small enough for the
	// offsets to bridge.
	if na != nb {
		return false
	}

	if gap := len(da) - len(db); gap < -1 || gap > 1 {
		return false
	}

	// Pa - Pb must equal kb - ka exactly.
	delta := kb - ka

	cmp := cmpDigits(da, db)
	if cmp == 0 {
		return delta == 0
	}

	hi, lo := da, db
	if cmp < 0 {
		hi, lo = db, da
	}

	diff, small := subDigits(hi, lo)
	if !small {
		// The magnitudes differ by at least 10^18, beyond any offset delta.
		return false
	}

	// Restore the sign of Pa - Pb: positive when the larger magnitude is
	// Pa's and the shared literal-exponent sign is positive, and flipped by
	// either condition reversing.
	//
	//nolint:gosec // subDigits caps diff below 10^18, well within int64.
	sd := int64(diff)
	if (cmp < 0) != na {
		sd = -sd
	}

	return sd == delta
}

// decExpParts splits a valid decimal literal's exact canonical exponent into
// the literal exponent digit run (leading zeros stripped; empty when the
// exponent is absent or zero), the run's sign, and the offset
// [DecCanonicalExp] adds to it (intLen - lead, where lead counts leading
// zeros across the integer and fraction digits). All O(len), with no
// [big.Int] built.
func decExpParts(s string) (string, bool, int64) {
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

	var (
		digits string
		neg    bool
	)

	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++

		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			neg = s[i] == '-'
			i++
		}

		for i < len(s) && s[i] == '0' {
			i++
		}

		digits = s[i:]
	}

	if digits == "" {
		// Canonical zero discards the sign so 0 and -0 agree.
		neg = false
	}

	return digits, neg, int64(intLen - lead)
}

// cmpDigits orders two decimal digit strings with no leading zeros: by
// length, then lexicographically.
func cmpDigits(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}

		return 1
	}

	return strings.Compare(a, b)
}

// subDigits returns hi - lo for two decimal digit strings with hi >= lo as
// numbers (no leading zeros), reporting small = false when the difference
// reaches 10^18; the returned magnitude is meaningful only when small. The
// subtraction runs right to left with a borrow, in O(len), so two nearly
// canceling multi-megabyte runs (10^n vs 10^n - 1) still compare in linear
// time.
func subDigits(hi, lo string) (uint64, bool) {
	const lowDigits = 18 // 10^18 fits int64 with room for negation

	var diff uint64

	pow := uint64(1)
	borrow := 0

	for i := range len(hi) {
		d := int(hi[len(hi)-1-i]-'0') - borrow
		if i < len(lo) {
			d -= int(lo[len(lo)-1-i] - '0')
		}

		borrow = 0
		if d < 0 {
			d += 10
			borrow = 1
		}

		if d != 0 {
			if i >= lowDigits {
				return 0, false
			}

			//nolint:gosec // d is a single decimal digit (1-9) here.
			diff += uint64(d) * pow
		}

		if i < lowDigits {
			pow *= 10
		}
	}

	return diff, true
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

// machineDigits bounds the significand length and the decimal shift that
// [DecNumber.Rat] handles in machine integers: 10^18 - 1 and 10^18 both fit
// an int64, and a significand of at most that many digits times 10^shift with
// the digit total at most machineDigits + 1 stays below 10^19, inside a
// uint64.
const machineDigits = 18

// Rat expands the canonical form into an exact rational. The cost is bounded
// only for ExactlyComparable values; callers must check that first.
func (d DecNumber) Rat() *big.Rat {
	if d.sig == "" {
		return new(big.Rat)
	}

	// Value = sig x 10^shift, where shift places the significand against the
	// decimal point; a shift of zero is an integer whose significand ends at
	// the point, the common shape of a JSON integer.
	shift := int64(d.exp) - int64(len(d.sig))

	r := new(big.Rat)

	switch {
	case len(d.sig) <= machineDigits && shift >= -machineDigits && int64(len(d.sig))+shift <= machineDigits+1:
		// The ordinary JSON number: the significand and the power of ten
		// both fit machine integers, so the rational is set from them
		// directly without the big-number scanner or exponentiation.
		u := digitsUint64(d.sig)

		if shift >= 0 {
			r.SetUint64(u * pow10(shift))
		} else {
			//nolint:gosec // u is below 10^18 and the power at most 10^18, both inside int64.
			r.SetFrac64(int64(u), int64(pow10(-shift)))
		}

	case shift == 0:
		r.SetInt(digitsBig(d.sig))

	case shift > 0:
		num := digitsBig(d.sig)
		pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(shift), nil)
		r.SetInt(num.Mul(num, pow))

	default:
		pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(-shift), nil)
		r.SetFrac(digitsBig(d.sig), pow)
	}

	if d.neg {
		r.Neg(r)
	}

	return r
}

// digitsUint64 reads a run of at most machineDigits decimal digits as a
// uint64.
func digitsUint64(digits string) uint64 {
	var u uint64

	for i := range len(digits) {
		u = u*10 + uint64(digits[i]-'0')
	}

	return u
}

// digitsBig reads a run of decimal digits of any length as a big integer.
func digitsBig(digits string) *big.Int {
	n := new(big.Int)
	n.SetString(digits, 10) // all digits, so this cannot fail

	return n
}

// pow10 returns 10^n for 0 <= n <= machineDigits, which fits a uint64.
func pow10(n int64) uint64 {
	pow := uint64(1)
	for range n {
		pow *= 10
	}

	return pow
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
// as d) is an exact multiple of the positive rational divisor m, at cost
// linear in the literal so an adversarial magnitude or precision is never
// expanded (see MaxNumberLen). Writing the value as sig*10^k with k >= 0 and
// m as the reduced p/q, the value is a multiple of m exactly when p divides it,
// since gcd(p, q) = 1 means q contributes no factor of p. The significand is
// reduced modulo p by chunked Horner rather than parsed into a [big.Int]
// (which is quadratic in the digit count), and the shift is handled by size:
// a shift within p's bit length contributes 10^k mod p directly, while a
// larger shift supplies every factor of 2 and 5 in p and leaves 10^k
// invertible modulo the remaining cofactor, so divisibility reduces to that
// cofactor dividing the significand, however long the exponent digit run is.
// The exponent comes from the literal because DecNumber.exp is clamped for an
// over-cap magnitude. The caller must pass an integral d (see
// [DecNumber.IsIntegral]) and a positive m.
func IntegerMultipleOf(d DecNumber, literal string, m *big.Rat) bool {
	p := new(big.Int).Abs(m.Num())
	if p.Sign() == 0 || d.sig == "" {
		return true // No real divisor, or a zero value: a multiple either way.
	}

	k, exact := integerShift(literal, len(d.sig))
	switch {
	case k < 0:
		return true // Not integral after all; the caller should have screened it.

	case k > int64(p.BitLen()):
		// The shift exceeds every possible power of 2 or 5 in p (a factor 2^a
		// or 5^b needs a, b <= log2(p)), so 10^k supplies all of them, and the
		// remaining cofactor r is coprime to 10, making 10^k invertible mod r:
		// p divides sig*10^k exactly when r divides sig. This branch also
		// covers a saturated shift, which only under-reports the true one.
		return decDigitsMod(d.sig, coprimeTenPart(p)).Sign() == 0

	case !exact:
		// Saturated yet not past p's bit length: reachable only for a
		// significand of ~2^50 digits. Fail open like the over-cap
		// non-integer skip rather than risk a wrong verdict.
		return true

	default:
		pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(k), p)
		rem := decDigitsMod(d.sig, p)
		rem.Mul(rem, pow)
		rem.Mod(rem, p)

		return rem.Sign() == 0
	}
}

// integerShiftSat is the saturation cap for integerShift's exponent
// accumulation. It only needs to exceed every possible bit length of a
// multipleOf numerator (a float64-derived rational stays under ~2100 bits);
// past it, every shift decides divisibility identically.
const integerShiftSat = int64(1) << 50

// integerShift returns k such that the literal denotes sig × 10^k (the
// canonical exponent minus the significand length), accumulating the exponent
// digit run with saturating arithmetic so the cost is O(len) and no [big.Int]
// is ever built. The exact result reports whether k is the true shift; when
// false the true shift is at least k (saturation only rounds down). A negative
// exponent saturating the cap reports k = -1 exactly: no physically possible
// significand can shift such a value back to an integer. The argument must
// already be a valid decimal literal (ParseDecNumber returned true).
func integerShift(literal string, sigLen int) (int64, bool) {
	s := literal

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

	var exp int64

	neg, exact := false, true

	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++

		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			neg = s[i] == '-'
			i++
		}

		for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			if exp >= integerShiftSat {
				exact = false

				continue
			}

			exp = exp*10 + int64(s[i]-'0')
		}
	}

	if neg {
		if !exact {
			return -1, true
		}

		exp = -exp
	}

	return exp + int64(intLen-lead) - int64(sigLen), exact
}

// decChunkDigits is the number of decimal digits decDigitsMod folds per
// Horner step: the widest power of ten whose chunk value fits a uint64.
const decChunkDigits = 18

// decDigitsMod reduces a decimal digit string modulo p (p > 0) with Horner's
// rule over fixed-size chunks, so every intermediate stays within a few words
// of p and the cost is linear in the digit count. Parsing the digits into a
// full [big.Int] first would be quadratic, which is exactly the expansion
// MaxNumberLen exists to prevent.
func decDigitsMod(digits string, p *big.Int) *big.Int {
	scale := new(big.Int).SetUint64(1e18) // 10^decChunkDigits
	rem := new(big.Int)
	chunk := new(big.Int)

	i := len(digits) % decChunkDigits
	if i > 0 {
		rem.SetUint64(foldDigits(digits[:i]))
	}

	for ; i < len(digits); i += decChunkDigits {
		chunk.SetUint64(foldDigits(digits[i : i+decChunkDigits]))

		rem.Mul(rem, scale)
		rem.Add(rem, chunk)
		rem.Mod(rem, p)
	}

	return rem.Mod(rem, p)
}

// foldDigits folds a run of decimal digits into its uint64 value; callers
// keep runs within decChunkDigits so the fold cannot overflow.
func foldDigits(digits string) uint64 {
	var v uint64

	for _, c := range []byte(digits) {
		v = v*10 + uint64(c-'0')
	}

	return v
}

// coprimeTenPart returns the largest divisor of p that is coprime to 10: p
// with every factor of 2 and 5 stripped.
func coprimeTenPart(p *big.Int) *big.Int {
	r := new(big.Int).Rsh(p, p.TrailingZeroBits())

	five := big.NewInt(5)
	q, rem := new(big.Int), new(big.Int)

	for {
		q.QuoRem(r, five, rem)

		if rem.Sign() != 0 {
			return r
		}

		r, q = q, r
	}
}
