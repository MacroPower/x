package numrat_test

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// TestIntegerMultipleOfHugeShift covers divisibility decisions where the
// literal's power-of-ten shift dwarfs the divisor: the shift supplies every
// factor of 2 and 5 in the divisor's numerator and leaves the remaining
// cofactor coprime to 10, so divisibility reduces to that cofactor dividing
// the significand.
func TestIntegerMultipleOfHugeShift(t *testing.T) {
	t.Parallel()

	// 10^5000: a 5001-digit integer whose canonical exponent is over the cap.
	pow10 := "1" + strings.Repeat("0", 5000)

	// (10^5000 - 1) * 10^100: an over-precise significand with a shift.
	ninesShifted := strings.Repeat("9", 5000) + "e100"

	tests := map[string]struct {
		literal string
		divisor *big.Rat
		want    bool
	}{
		"power of ten not divisible by 3": {
			// 10^n = 1 (mod 3) at any magnitude.
			literal: pow10, divisor: big.NewRat(3, 1), want: false,
		},
		"power of ten not divisible by 6": {
			// 10^n = 4 (mod 6) for n >= 1: the factor 2 divides, the 3 never does.
			literal: pow10, divisor: big.NewRat(6, 1), want: false,
		},
		"power of ten divisible by 40": {
			// 40 = 2^3 * 5: entirely supplied by the shift.
			literal: pow10, divisor: big.NewRat(40, 1), want: true,
		},
		"power of ten divisible by five halves": {
			// The reduced numerator 5 is supplied by the shift.
			literal: pow10, divisor: big.NewRat(5, 2), want: true,
		},
		"power of ten not divisible by 7": {
			// 10 has order 6 mod 7 and 5000 = 2 (mod 6), so 10^5000 = 2 (mod 7).
			literal: pow10, divisor: big.NewRat(7, 1), want: false,
		},
		"shifted nines divisible by 3": {
			// The digit sum of the significand is divisible by 3.
			literal: ninesShifted, divisor: big.NewRat(3, 1), want: true,
		},
		"shifted nines not divisible by 7": {
			// 10^5000 - 1 = 1 (mod 7) and the shift is coprime to 7.
			literal: ninesShifted, divisor: big.NewRat(7, 1), want: false,
		},
		"over-cap exponent digit run divisible by 2": {
			literal: "1e" + strings.Repeat("9", 5000), divisor: big.NewRat(2, 1), want: true,
		},
		"over-cap exponent digit run not divisible by 3": {
			literal: "1e" + strings.Repeat("9", 5000), divisor: big.NewRat(3, 1), want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d, ok := numrat.ParseDecNumber(tc.literal)
			require.True(t, ok)
			require.True(t, d.IsIntegral(),
				"IntegerMultipleOf requires an integral value")
			require.False(t, d.ExactlyComparable(),
				"input must be over-cap so it exercises the guarded path")
			assert.Equal(t, tc.want,
				numrat.IntegerMultipleOf(d, tc.literal, tc.divisor))
		})
	}
}

// TestIntegerMultipleOfAdversarialCost pins the DoS guard: a multi-megabyte
// significand or exponent digit run must be decided in time linear in the
// literal, never expanded through the quadratic big.Int parse (which takes
// tens of seconds at this size).
func TestIntegerMultipleOfAdversarialCost(t *testing.T) {
	t.Parallel()

	const digits = 8 << 20

	tests := map[string]struct {
		literal string
		divisor *big.Rat
		want    bool
	}{
		"huge significand": {
			// The digit sum of 8Mi nines is divisible by 9, so by 3.
			literal: strings.Repeat("9", digits),
			divisor: big.NewRat(3, 1),
			want:    true,
		},
		"huge exponent digit run": {
			// 10^k is never a multiple of 3, at any magnitude.
			literal: "1e" + strings.Repeat("9", digits),
			divisor: big.NewRat(3, 1),
			want:    false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d, ok := numrat.ParseDecNumber(tc.literal)
			require.True(t, ok)
			require.True(t, d.IsIntegral(),
				"IntegerMultipleOf requires an integral value")
			require.False(t, d.ExactlyComparable(),
				"input must be over-cap so it exercises the guarded path")

			start := time.Now()
			got := numrat.IntegerMultipleOf(d, tc.literal, tc.divisor)
			elapsed := time.Since(start)

			assert.Equal(t, tc.want, got)
			assert.Less(t, elapsed, 10*time.Second,
				"an adversarial literal must never reach a quadratic expansion")
		})
	}
}
