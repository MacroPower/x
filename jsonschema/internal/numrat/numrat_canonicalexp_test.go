package numrat_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// DecCanonicalExpEqual decides in O(len) whether two literals share an exact
// canonical exponent, without materializing the exponent digit run into a
// big.Int (DecCanonicalExp's SetString is quadratic in the run length). The
// crafted cases pin the huge-run branches: same-run identity, offset
// compensation across different significand shapes, borrow chains spanning
// the whole run, and near-misses that only the exact comparison can reject.

func TestDecCanonicalExpEqual(t *testing.T) {
	t.Parallel()

	// A 40-digit exponent run, over the 32-digit bound for the exact big.Int
	// path, so these pairs exercise the linear comparison.
	run := "1" + strings.Repeat("0", 39)              // 10^39
	runMinusOne := strings.Repeat("9", 39)            // 10^39 - 1
	runPlusOne := "1" + strings.Repeat("0", 38) + "1" // 10^39 + 1

	tests := map[string]struct {
		a, b string
		want bool
	}{
		"identical huge runs": {
			a:    "1e" + run,
			b:    "1e" + run,
			want: true,
		},
		"huge runs differing in the last digit": {
			a:    "1e" + run,
			b:    "1e" + runPlusOne,
			want: false,
		},
		"offset compensates a one-smaller run with a full borrow": {
			// 1e(10^39) and 10e(10^39 - 1) both denote 0.1 x 10^(10^39 + 1).
			a:    "1e" + run,
			b:    "10e" + runMinusOne,
			want: true,
		},
		"offset compensation in the negative domain": {
			// 1e-(10^39) and 0.1e-(10^39 - 1) both sit at exponent
			// -(10^39) + 1.
			a:    "1e-" + run,
			b:    "0.1e-" + runMinusOne,
			want: true,
		},
		"fraction leading zeros shift the offset": {
			// Both denote 0.5 x 10^(10^39 - 1): the extra fractional zero in
			// 0.05 lowers the offset by one, and the run compensates.
			a:    "0.05e" + run,
			b:    "0.5e" + runMinusOne,
			want: true,
		},
		"offset does not bridge a same-direction difference": {
			// The run grows by one and the offset by one more (1 -> 10), so
			// the exponents land two apart, not together.
			a:    "1e" + run,
			b:    "10e" + runPlusOne,
			want: false,
		},
		"opposite exponent signs": {
			a:    "1e" + run,
			b:    "1e-" + run,
			want: false,
		},
		"huge run against a missing exponent": {
			a:    "1e" + run,
			b:    "1",
			want: false,
		},
		"leading zeros in the run are canonical": {
			a:    "1e" + strings.Repeat("0", 7) + run,
			b:    "1e" + run,
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, numrat.DecCanonicalExpEqual(tc.a, tc.b))
			assert.Equal(t, tc.want, numrat.DecCanonicalExpEqual(tc.b, tc.a), "must be symmetric")
		})
	}
}

// TestDecCanonicalExpEqualMatchesBigInt cross-checks the linear comparison
// against the exact big.Int form over every pair from a component grid:
// varied significand shapes (shifting the intLen-lead offset) crossed with
// exponent runs on both sides of the 32-digit bound, in both signs.
func TestDecCanonicalExpEqualMatchesBigInt(t *testing.T) {
	t.Parallel()

	sigs := []string{"1", "10", "1200", "0.1", "0.005", "12.34", "5."}

	runs := []string{
		"", "5", "37", "1000",
		"1" + strings.Repeat("0", 35),
		strings.Repeat("9", 35),
		"1" + strings.Repeat("0", 34) + "1",
		"2" + strings.Repeat("0", 34),
		strings.Repeat("0", 4) + "1" + strings.Repeat("0", 35),
	}

	var literals []string

	for _, sig := range sigs {
		for _, run := range runs {
			if run == "" {
				literals = append(literals, sig)

				continue
			}

			literals = append(literals, sig+"e"+run, sig+"e-"+run, sig+"E+"+run)
		}
	}

	for _, a := range literals {
		for _, b := range literals {
			want := numrat.DecCanonicalExp(a).Cmp(numrat.DecCanonicalExp(b)) == 0
			assert.Equal(t, want, numrat.DecCanonicalExpEqual(a, b), "a=%s b=%s", a, b)
		}
	}
}
