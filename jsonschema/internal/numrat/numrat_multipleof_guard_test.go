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
// tens of seconds at this size). A fixed wall-clock bound cannot separate the
// two paths on every machine -- race and coverage instrumentation slow the
// linear path past any constant tight enough to catch the quadratic one on
// fast hardware -- so each case calibrates its budget from the same decision
// at 1/32 the size. Linear scaling stays well inside the scaled budget on any
// machine, and a quadratic expansion overshoots it by orders of magnitude.
func TestIntegerMultipleOfAdversarialCost(t *testing.T) {
	t.Parallel()

	const (
		digits   = 8 << 20
		refScale = 32

		// The budget grants 4x headroom over exact linear scaling
		// (4 * refScale); the quadratic parse costs refScale times more
		// than the budget allows.
		budgetScale = 4 * refScale

		// The floor absorbs timer noise when the reference run is too
		// fast to measure reliably.
		refFloor = 100 * time.Millisecond
	)

	tests := map[string]struct {
		literal string
		refLit  string
		divisor *big.Rat
		want    bool
	}{
		"huge significand": {
			// The digit sum of a run of nines is divisible by 9, so by 3.
			literal: strings.Repeat("9", digits),
			refLit:  strings.Repeat("9", digits/refScale),
			divisor: big.NewRat(3, 1),
			want:    true,
		},
		"huge exponent digit run": {
			// 10^k is never a multiple of 3, at any magnitude.
			literal: "1e" + strings.Repeat("9", digits),
			refLit:  "1e" + strings.Repeat("9", digits/refScale),
			divisor: big.NewRat(3, 1),
			want:    false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, refElapsed := timeIntegerMultipleOf(t, tc.refLit, tc.divisor)
			budget := budgetScale * max(refElapsed, refFloor)

			got, elapsed := timeIntegerMultipleOf(t, tc.literal, tc.divisor)

			assert.Equal(t, tc.want, got)
			assert.Less(t, elapsed, budget,
				"an adversarial literal must never reach a quadratic expansion")
		})
	}
}

// timeIntegerMultipleOf parses literal, verifies it exercises the guarded
// over-cap path, and returns the divisibility verdict together with the time
// IntegerMultipleOf alone took.
func timeIntegerMultipleOf(t *testing.T, literal string, divisor *big.Rat) (bool, time.Duration) {
	t.Helper()

	d, ok := numrat.ParseDecNumber(literal)
	require.True(t, ok)
	require.True(t, d.IsIntegral(),
		"IntegerMultipleOf requires an integral value")
	require.False(t, d.ExactlyComparable(),
		"input must be over-cap so it exercises the guarded path")

	start := time.Now()
	got := numrat.IntegerMultipleOf(d, literal, divisor)

	return got, time.Since(start)
}
