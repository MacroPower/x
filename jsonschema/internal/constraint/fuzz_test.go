package constraint_test

import (
	"errors"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// FuzzIntervalAdmitsVsBigRat asserts that the interval a schema's four numeric
// bound keywords describe admits an instance exactly when exact arithmetic
// does: the bound side is the shortest decimal of the float64 the keyword
// holds, the instance side is the exact decimal literal, and the verdict is
// the big.Rat comparison of the two. A non-finite bound has no rational form
// and is left unset, as JSON cannot spell it.
func FuzzIntervalAdmitsVsBigRat(f *testing.F) {
	f.Add(0.0, 1.0, 0.0, 1.0, uint8(0b0101), "0.5")
	f.Add(0.1, 0.0, 0.0, 0.0, uint8(0b0001), "0.1")
	f.Add(0.0, 0.0, 0.1, 0.0, uint8(0b0100), "0.1")
	f.Add(0.0, 0.0, 0.0, 0.1, uint8(0b1000), "0.10000000000000000555")
	f.Add(9007199254740992.0, 0.0, 0.0, 0.0, uint8(0b0001), "9007199254740993")
	f.Add(0.0, 0.0, 0.0, 1e308, uint8(0b1000), "1e400")
	f.Add(math.Copysign(0, -1), 0.0, 0.0, 0.0, uint8(0b0001), "-0")
	f.Add(0.0, 0.0, 0.0, 0.0, uint8(0b0000), "abc")

	f.Fuzz(func(t *testing.T, minimum, exclusiveMinimum, maximum, exclusiveMaximum float64, set uint8, literal string) {
		schema := &jsonschema.Schema{}

		bounds := []struct {
			field **float64
			val   float64
			lower bool
			incl  bool
		}{
			{&schema.Minimum, minimum, true, true},
			{&schema.ExclusiveMinimum, exclusiveMinimum, true, false},
			{&schema.Maximum, maximum, false, true},
			{&schema.ExclusiveMaximum, exclusiveMaximum, false, false},
		}

		for i := range bounds {
			if set&(1<<i) == 0 || math.IsNaN(bounds[i].val) || math.IsInf(bounds[i].val, 0) {
				continue
			}

			v := bounds[i].val
			*bounds[i].field = &v
		}

		// The instance is a JSON number literal, which is what the validator
		// compares; a spelling outside the JSON grammar has no document form
		// and is admitted as a non-number. The oracle side reads the literal
		// exactly through big.Rat, which expands an exponent in full, so the
		// literal is capped where the validator's own decimal cap sits well
		// below it.
		if !jsonv1.Valid([]byte(literal)) || len(literal) > 64 {
			t.Skip("not a JSON number literal the oracle reads")
		}

		if _, ok := numrat.ParseDecNumber(literal); !ok {
			t.Skip("not a number")
		}

		instance, ok := exactRat(literal)
		if !ok {
			t.Skip("exponent beyond the oracle")
		}

		want := true

		for i := range bounds {
			if *bounds[i].field == nil {
				continue
			}

			c := instance.Cmp(numrat.Float64ToRat(**bounds[i].field))
			if !bounds[i].lower {
				c = -c
			}

			want = want && (c > 0 || (c == 0 && bounds[i].incl))
		}

		got := constraint.NumericInterval(schema).Admits(jsonv1.Number(literal))
		require.Equalf(t, want, got,
			"Admits(%s) under minimum=%v exclusiveMinimum=%v maximum=%v exclusiveMaximum=%v (set %04b)",
			literal, minimum, exclusiveMinimum, maximum, exclusiveMaximum, set)
	})
}

// FuzzComposeMultipleOfVsBigRat asserts the composed divisor is the least
// common multiple of the two shortest-decimal rationals, and that the boolean
// is true exactly when that rational is a float64's shortest decimal. A
// non-finite or non-positive input keeps b and is excluded.
func FuzzComposeMultipleOfVsBigRat(f *testing.F) {
	f.Add(0.1, 0.2)
	f.Add(2.0, 3.0)
	f.Add(0.5, 0.25)
	f.Add(1e300, 3.0)
	f.Add(1.0/3.0, 1.0)
	f.Add(0.0, 1.0)
	f.Add(math.Inf(1), 1.0)

	f.Fuzz(func(t *testing.T, a, b float64) {
		ra, rb := numrat.Float64ToRat(a), numrat.Float64ToRat(b)
		if ra == nil || rb == nil || ra.Sign() <= 0 || rb.Sign() <= 0 {
			got, ok := constraint.ComposeMultipleOf(a, b)
			require.True(t, ok)
			require.Equal(t, math.Float64bits(b), math.Float64bits(got), "a non-finite or non-positive input keeps b")

			return
		}

		// Lcm(p1/q1, p2/q2) = lcm(p1*q2, p2*q1) / (q1*q2).
		n1 := new(big.Int).Mul(ra.Num(), rb.Denom())
		n2 := new(big.Int).Mul(rb.Num(), ra.Denom())
		gcd := new(big.Int).GCD(nil, nil, n1, n2)
		num := new(big.Int).Mul(n1, n2)
		num.Quo(num, gcd)

		lcm := new(big.Rat).SetFrac(num, new(big.Int).Mul(ra.Denom(), rb.Denom()))

		got, ok := constraint.ComposeMultipleOf(a, b)

		wantFloat, _ := lcm.Float64()
		wantOK := !math.IsInf(wantFloat, 0)

		if wantOK {
			wantOK = numrat.Float64ToRat(wantFloat).Cmp(lcm) == 0
		}

		require.Equalf(t, wantOK, ok, "ComposeMultipleOf(%v, %v) = %v: lcm %s", a, b, got, lcm.RatString())

		if ok {
			require.Equalf(t, math.Float64bits(wantFloat), math.Float64bits(got),
				"ComposeMultipleOf(%v, %v): lcm %s", a, b, lcm.RatString())
		}
	})
}

// FuzzParseNumericBoundVsStrconv asserts an accepted literal's endpoint is
// the value strconv and big.Float agree on, and that a refusal follows the
// two-case rule: the grammar refuses first (a JSON integer on an integer
// kind, the decimal float grammar on a float kind), and a literal the grammar
// admits is refused with ErrNotRepresentable exactly when it is not exact at
// the kind's width or outside its range.
func FuzzParseNumericBoundVsStrconv(f *testing.F) {
	for _, lit := range []string{
		"0", "-0", "1", "-1", "010", "+1", "1.5", "1e3", "0x10", "1_000",
		"9007199254740992", "9007199254740993", "-9007199254740993",
		"18446744073709551615", "18446744073709551616", "0.1", "10.0000001",
		"1e400", "1e-400", "NaN", "Inf", "", "abc", "1.0", "1152921504606846976",
	} {
		for _, kind := range []uint8{uint8(reflect.Int), uint8(reflect.Uint8), uint8(reflect.Float32), uint8(reflect.Float64)} {
			f.Add(lit, kind)
		}
	}

	f.Fuzz(func(t *testing.T, literal string, kindByte uint8) {
		kind := boundKinds[int(kindByte)%len(boundKinds)]

		end, err := constraint.ParseNumericBound(literal, kind)

		switch kind {
		case reflect.Float32, reflect.Float64:
			checkFloatBound(t, literal, kind, end, err)
		default:
			checkIntegerBound(t, literal, kind, end, err)
		}
	})
}

// boundKinds are the kinds the bound parser distinguishes.
var boundKinds = []reflect.Kind{
	reflect.Int, reflect.Int8, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint64,
	reflect.Float32, reflect.Float64,
}

// checkIntegerBound applies the integer-kind rule: the JSON integer grammar
// first, then the kind's sign, then the 2^53 magnitude cap.
func checkIntegerBound(t *testing.T, literal string, kind reflect.Kind, end constraint.Endpoint, err error) {
	t.Helper()

	grammarErr := constraint.CheckIntegerLiteral(literal)
	if grammarErr != nil {
		require.ErrorIsf(t, err, constraint.ErrIntegerLiteral, "%q on %s", literal, kind)

		return
	}

	n, ok := new(big.Int).SetString(literal, 10)
	require.Truef(t, ok, "%q passed the integer grammar but big.Int refuses it", literal)

	unsigned := kind == reflect.Uint || kind == reflect.Uint8 || kind == reflect.Uint64
	if unsigned && n.Sign() < 0 {
		require.Errorf(t, err, "%q on %s must be refused as negative", literal, kind)
		require.NotErrorIsf(
			t,
			err,
			constraint.ErrNotRepresentable,
			"%q on %s is a sign refusal, not a width one",
			literal,
			kind,
		)

		return
	}

	if n.CmpAbs(big.NewInt(1<<53)) > 0 || (unsigned && !n.IsUint64()) || (!unsigned && !n.IsInt64()) {
		require.ErrorIsf(t, err, constraint.ErrNotRepresentable, "%q on %s", literal, kind)

		return
	}

	require.NoErrorf(t, err, "%q on %s", literal, kind)
	require.Zerof(t, new(big.Rat).SetInt(n).Cmp(end.Rat), "%q on %s: endpoint %s", literal, kind, end.Rat)

	want, _ := new(big.Float).SetInt(n).Float64()
	require.Equalf(t, math.Float64bits(want), math.Float64bits(end.Val), "%q on %s", literal, kind)
	require.True(t, end.Inclusive)
}

// checkFloatBound applies the float-kind rule: the decimal float grammar
// first, then exactness at the kind's width and the range check.
func checkFloatBound(t *testing.T, literal string, kind reflect.Kind, end constraint.Endpoint, err error) {
	t.Helper()

	// The spelling policy also refuses a literal past float64's range, and
	// that refusal is the width one, judged below with the rest.
	n, grammarErr := constraint.ParseDecimalFloat(literal)
	if grammarErr != nil && !errors.Is(grammarErr, constraint.ErrNotRepresentable) {
		require.Errorf(t, err, "%q on %s", literal, kind)
		require.NotErrorIsf(t, err, constraint.ErrNotRepresentable, "%q on %s is a grammar refusal", literal, kind)

		return
	}

	if !floatExact(literal, kind) {
		require.ErrorIsf(t, err, constraint.ErrNotRepresentable, "%q on %s", literal, kind)

		return
	}

	require.NoErrorf(t, err, "%q on %s", literal, kind)

	want := n
	if want == 0 {
		want = 0
	}

	require.Equalf(t, math.Float64bits(want), math.Float64bits(end.Val), "%q on %s", literal, kind)
	require.Zerof(t, numrat.Float64ToRat(want).Cmp(end.Rat), "%q on %s: endpoint %s", literal, kind, end.Rat)
	require.True(t, end.Inclusive)
}

// floatExact is the oracle for the width check: the literal must parse at
// the kind's width without overflow and be the shortest decimal of the value
// it parses to, so a value of the kind renders as the literal. The parse is
// strconv's and the shortest decimal is big.Float's independent rendering
// of the same value, so the two agree on what "exact" means here without
// consulting the package under test.
func floatExact(literal string, kind reflect.Kind) bool {
	width := 64
	if kind == reflect.Float32 {
		width = 32
	}

	parsed, err := strconv.ParseFloat(literal, width)
	if err != nil {
		return false
	}

	if math.IsInf(parsed, 0) || math.IsNaN(parsed) {
		return false
	}

	shortest, ok := new(big.Rat).SetString(strconv.FormatFloat(parsed, 'g', -1, width))
	if !ok {
		return false
	}

	// A literal the oracle cannot read has a non-zero mantissa under an
	// exponent past a million, and no float's shortest decimal is one.
	given, ok := exactRat(literal)

	return ok && shortest.Cmp(given) == 0
}

// exactRat reads a decimal literal as an exact rational. The big.Rat parser
// refuses an exponent past a million even under a zero mantissa, where the
// value is zero whatever the exponent, so that case is answered here. Any
// other literal past the limit is reported as unread.
func exactRat(literal string) (*big.Rat, bool) {
	if r, ok := new(big.Rat).SetString(literal); ok {
		return r, true
	}

	mantissa := literal
	if i := strings.IndexAny(literal, "eE"); i >= 0 {
		mantissa = literal[:i]
	}

	if strings.Trim(mantissa, "+-0.") == "" && strings.ContainsAny(mantissa, "0") {
		return new(big.Rat), true
	}

	return nil, false
}

// FuzzParseUnsignedLiteralVsStrconv asserts -0 reads as zero, any other sign
// or a fraction is refused, a value past the width is refused with
// ErrNotRepresentable, and an accepted value is what strconv.ParseUint reads
// at the same width.
func FuzzParseUnsignedLiteralVsStrconv(f *testing.F) {
	for _, lit := range []string{"0", "-0", "-1", "+1", "1", "255", "256", "010", "1.5", "1e3", "", "18446744073709551615", "18446744073709551616"} {
		f.Add(lit, uint8(8))
		f.Add(lit, uint8(64))
	}

	f.Fuzz(func(t *testing.T, literal string, width uint8) {
		bitSize := []int{8, 16, 32, 64}[int(width)%4]

		got, err := constraint.ParseUnsignedLiteral(literal, bitSize)

		grammarErr := constraint.CheckIntegerLiteral(literal)
		if grammarErr != nil {
			require.ErrorIsf(t, err, constraint.ErrIntegerLiteral, "%q at %d bits", literal, bitSize)

			return
		}

		if literal == "-0" {
			require.NoError(t, err)
			require.Zero(t, got)

			return
		}

		if literal[0] == '-' {
			require.Errorf(t, err, "%q at %d bits", literal, bitSize)

			return
		}

		want, strconvErr := strconv.ParseUint(literal, 10, bitSize)
		if strconvErr != nil {
			var numErr *strconv.NumError

			require.ErrorAsf(t, strconvErr, &numErr, "%q at %d bits", literal, bitSize)
			require.ErrorIsf(
				t,
				strconvErr,
				strconv.ErrRange,
				"%q at %d bits: strconv refuses for a reason other than range",
				literal,
				bitSize,
			)
			require.ErrorIsf(t, err, constraint.ErrNotRepresentable, "%q at %d bits", literal, bitSize)

			return
		}

		require.NoErrorf(t, err, "%q at %d bits", literal, bitSize)
		require.Equalf(t, want, got, "%q at %d bits", literal, bitSize)
	})
}
