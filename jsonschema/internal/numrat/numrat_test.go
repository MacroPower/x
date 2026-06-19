package numrat_test

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// Edge-case tests for the exact-decimal numeric core, exercised directly. How
// the validator wires these conversions into the numeric keywords is covered by
// the parent package's own tests. Unexported behavior (the DoS clamp, the
// magnitude-class branches) is reached through the exported surface
// [numrat.ParseDecNumber], [numrat.DecNumber.CmpRat], and
// [numrat.IntegerMultipleOf].

func TestParseDecNumberCanonicalForm(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		ok    bool
		// Want holds the rational the literal expands to; only checked when
		// the value is ExactlyComparable.
		want *big.Rat
		zero bool
	}{
		"plain integer": {
			input: "42",
			ok:    true,
			want:  big.NewRat(42, 1),
		},
		"trailing zeros strip": {
			input: "4200e-2",
			ok:    true,
			want:  big.NewRat(42, 1),
		},
		"fraction shortest decimal": {
			input: "1.01",
			ok:    true,
			want:  big.NewRat(101, 100),
		},
		"negative fraction": {
			input: "-0.5",
			ok:    true,
			want:  big.NewRat(-1, 2),
		},
		"leading plus accepted": {
			input: "+3",
			ok:    true,
			want:  big.NewRat(3, 1),
		},
		"bare leading dot": {
			input: ".5",
			ok:    true,
			want:  big.NewRat(1, 2),
		},
		"bare trailing dot": {
			input: "5.",
			ok:    true,
			want:  big.NewRat(5, 1),
		},
		"signed zero canonicalizes": {
			input: "-0",
			ok:    true,
			zero:  true,
		},
		"zero with exponent canonicalizes": {
			input: "0e5",
			ok:    true,
			zero:  true,
		},
		"empty rejected": {
			input: "",
			ok:    false,
		},
		"hex rejected": {
			input: "0x1f",
			ok:    false,
		},
		"fraction form rejected": {
			input: "1/2",
			ok:    false,
		},
		"bare exponent rejected": {
			input: "1e",
			ok:    false,
		},
		"trailing junk rejected": {
			input: "1.2.3",
			ok:    false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d, ok := numrat.ParseDecNumber(tc.input)
			require.Equal(t, tc.ok, ok, "parse acceptance for %q", tc.input)

			if !tc.ok {
				return
			}

			if tc.zero {
				assert.Empty(t, d.Sig(), "canonical zero has empty significand")
				assert.True(t, d.IsIntegral(), "zero is integral")
				assert.Equal(t, 0, d.Rat().Sign(), "zero expands to 0/1")

				return
			}

			require.True(t, d.ExactlyComparable(),
				"in-range literal must be exactly comparable")
			assert.Zero(t, tc.want.Cmp(d.Rat()),
				"%q expands to %v, want %v", tc.input, d.Rat(), tc.want)
		})
	}
}

func TestParseDecNumberDoSBounds(t *testing.T) {
	t.Parallel()

	// A literal whose significand or decimal exponent exceeds MaxNumberLen must
	// parse (so the value is usable) yet report not ExactlyComparable, so the
	// validator never expands it through the quadratic big.Rat path.
	huge := "1e" + strings.Repeat("9", 12) // exponent far past the clamp
	longSig := strings.Repeat("9", numrat.MaxNumberLen+10)

	tests := map[string]struct {
		input             string
		exactlyComparable bool
		integral          bool
	}{
		"huge positive exponent": {
			input:             huge,
			exactlyComparable: false,
			integral:          true,
		},
		"huge negative exponent": {
			input:             "1e-" + strings.Repeat("9", 12),
			exactlyComparable: false,
			integral:          false,
		},
		"over-length integral significand": {
			input:             longSig,
			exactlyComparable: false,
			integral:          true,
		},
		"at the significand cap stays comparable": {
			input:             strings.Repeat("9", numrat.MaxNumberLen),
			exactlyComparable: true,
			integral:          true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d, ok := numrat.ParseDecNumber(tc.input)
			require.True(t, ok, "DoS-bound literal must still parse")
			assert.Equal(t, tc.exactlyComparable, d.ExactlyComparable(),
				"ExactlyComparable for %s", name)
			assert.Equal(t, tc.integral, d.IsIntegral(),
				"IsIntegral for %s", name)
		})
	}
}

func TestIsIntegral(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		want  bool
	}{
		"integer":              {input: "10", want: true},
		"integer via exponent": {input: "1e3", want: true},
		"point-zero":           {input: "5.0", want: true},
		"fraction":             {input: "1.5", want: false},
		"tiny fraction":        {input: "1e-3", want: false},
		"zero":                 {input: "0", want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d, ok := numrat.ParseDecNumber(tc.input)
			require.True(t, ok)
			assert.Equal(t, tc.want, d.IsIntegral())
		})
	}
}

func TestJSONNumberIsIntegral(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input json.Number
		want  bool
	}{
		"plain integer":   {input: json.Number("7"), want: true},
		"point-zero":      {input: json.Number("7.0"), want: true},
		"exponent form":   {input: json.Number("1e3"), want: true},
		"beyond int64":    {input: json.Number("1e30"), want: true},
		"fraction":        {input: json.Number("7.25"), want: false},
		"not a number":    {input: json.Number("abc"), want: false},
		"empty is no int": {input: json.Number(""), want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, numrat.JSONNumberIsIntegral(tc.input))
		})
	}
}

func TestIsIntegralInstance(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input any
		want  bool
	}{
		"small json.Number int": {input: json.Number("3"), want: true},
		"large integral float":  {input: 1e30, want: true},
		"large json.Number int": {input: json.Number("1e30"), want: true},
		"float with fraction":   {input: 1.5, want: false},
		"json.Number fraction":  {input: json.Number("1.5"), want: false},
		"string is not numeric": {input: "3", want: false},
		"nil is not numeric":    {input: nil, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, numrat.IsIntegralInstance(tc.input))
		})
	}
}

func TestCmpRatMagnitudeClasses(t *testing.T) {
	t.Parallel()

	// Each input is a literal that is not ExactlyComparable, ordered against an
	// in-range float64 bound. CmpRat must return strictly -1 or +1 (equality is
	// impossible against a float64) per its magnitude-class reasoning.
	overPrecise := "1." + strings.Repeat("0", numrat.MaxNumberLen) + "1"

	tests := map[string]struct {
		input string
		bound *big.Rat
		want  int
	}{
		"huge positive above bound": {
			input: "1e" + strings.Repeat("9", 12),
			bound: big.NewRat(1000, 1),
			want:  1,
		},
		"huge negative below bound": {
			input: "-1e" + strings.Repeat("9", 12),
			bound: big.NewRat(-1000, 1),
			want:  -1,
		},
		"tiny positive above zero bound": {
			input: "1e-" + strings.Repeat("9", 12),
			bound: big.NewRat(0, 1),
			want:  1,
		},
		"tiny positive below positive bound": {
			input: "1e-" + strings.Repeat("9", 12),
			bound: big.NewRat(1, 2),
			want:  -1,
		},
		"tiny negative below zero bound": {
			input: "-1e-" + strings.Repeat("9", 12),
			bound: big.NewRat(0, 1),
			want:  -1,
		},
		"over-precise just above one": {
			input: overPrecise,
			bound: big.NewRat(1, 1),
			want:  1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d, ok := numrat.ParseDecNumber(tc.input)
			require.True(t, ok)
			require.False(t, d.ExactlyComparable(),
				"input must reach the magnitude-class path")
			assert.Equal(t, tc.want, d.CmpRat(tc.bound),
				"CmpRat must never report equality against a float64 bound")
		})
	}
}

func TestIntegerMultipleOf(t *testing.T) {
	t.Parallel()

	// The literal carries the exact exponent the modular check reads, which is
	// why an over-cap magnitude still decides divisibility without expansion.
	tests := map[string]struct {
		literal string
		divisor *big.Rat
		want    bool
	}{
		"6 is a multiple of 3":      {literal: "6", divisor: big.NewRat(3, 1), want: true},
		"7 is not a multiple of 3":  {literal: "7", divisor: big.NewRat(3, 1), want: false},
		"100 is a multiple of 4":    {literal: "100", divisor: big.NewRat(4, 1), want: true},
		"multiple of fraction half": {literal: "3", divisor: big.NewRat(1, 2), want: true},
		"non-multiple of two-fifths": {
			literal: "3", divisor: big.NewRat(2, 5), want: false,
		},
		"zero divides anything": {literal: "5", divisor: big.NewRat(0, 1), want: true},
		"zero value is a multiple": {
			literal: "0", divisor: big.NewRat(3, 1), want: true,
		},
		"over-cap power of ten divisible": {
			literal: "1e" + strings.Repeat("9", 12), divisor: big.NewRat(2, 1), want: true,
		},
		"over-cap power of ten odd divisor": {
			// 10^k is never a multiple of 3, at any magnitude.
			literal: "1e" + strings.Repeat("9", 12), divisor: big.NewRat(3, 1), want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d, ok := numrat.ParseDecNumber(tc.literal)
			require.True(t, ok)
			require.True(t, d.IsIntegral(),
				"IntegerMultipleOf requires an integral value")
			assert.Equal(t, tc.want,
				numrat.IntegerMultipleOf(d, tc.literal, tc.divisor))
		})
	}
}

func TestSchemaNumberRat(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input any
		ok    bool
		want  *big.Rat
	}{
		"float shortest decimal": {
			input: 0.1,
			ok:    true,
			want:  big.NewRat(1, 10),
		},
		"int kind": {
			input: 7,
			ok:    true,
			want:  big.NewRat(7, 1),
		},
		"uint kind": {
			input: uint8(255),
			ok:    true,
			want:  big.NewRat(255, 1),
		},
		"json.Number in range": {
			input: json.Number("1.5"),
			ok:    true,
			want:  big.NewRat(3, 2),
		},
		"over-cap json.Number rejected": {
			input: json.Number("1e" + strings.Repeat("9", 12)),
			ok:    false,
		},
		"string is not numeric": {
			input: "5",
			ok:    false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := numrat.SchemaNumberRat(tc.input)
			require.Equal(t, tc.ok, ok)

			if !tc.ok {
				return
			}

			assert.Zero(t, tc.want.Cmp(got), "got %v, want %v", got, tc.want)
		})
	}
}

func TestEnumMemberRats(t *testing.T) {
	t.Parallel()

	t.Run("nil when no member is numeric", func(t *testing.T) {
		t.Parallel()

		assert.Nil(t, numrat.EnumMemberRats([]any{"a", true, nil}))
	})

	t.Run("aligned by index with nil for non-numeric", func(t *testing.T) {
		t.Parallel()

		rats := numrat.EnumMemberRats([]any{"a", 2, json.Number("3.5")})
		require.Len(t, rats, 3)
		assert.Nil(t, rats[0], "non-numeric member has no rational")
		require.NotNil(t, rats[1])
		assert.Zero(t, big.NewRat(2, 1).Cmp(rats[1]))
		require.NotNil(t, rats[2])
		assert.Zero(t, big.NewRat(7, 2).Cmp(rats[2]))
	})
}

func TestToBigRatAndIsNumeric(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input     any
		isNumeric bool
		ratOK     bool
		want      *big.Rat
	}{
		"float64": {
			input: 1.25, isNumeric: true, ratOK: true, want: big.NewRat(5, 4),
		},
		"json.Number": {
			input: json.Number("2.5"), isNumeric: true, ratOK: true, want: big.NewRat(5, 2),
		},
		"over-cap json.Number": {
			input: json.Number("1e" + strings.Repeat("9", 12)), isNumeric: true, ratOK: false,
		},
		"string": {
			input: "1", isNumeric: false, ratOK: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.isNumeric, numrat.IsNumeric(tc.input))

			got, ok := numrat.ToBigRat(tc.input)
			require.Equal(t, tc.ratOK, ok)

			if tc.ratOK {
				assert.Zero(t, tc.want.Cmp(got))
			}
		})
	}
}

func TestFloat64ToRat(t *testing.T) {
	t.Parallel()

	t.Run("shortest decimal avoids binary artifacts", func(t *testing.T) {
		t.Parallel()

		assert.Zero(t, big.NewRat(11, 10).Cmp(numrat.Float64ToRat(1.1)),
			"1.1 must expand as 11/10, not its binary expansion")
	})

	t.Run("non-finite has no rational form", func(t *testing.T) {
		t.Parallel()

		inf := 1.0
		inf /= 0.0
		assert.Nil(t, numrat.Float64ToRat(inf))
	})
}
