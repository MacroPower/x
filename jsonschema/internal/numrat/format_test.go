package numrat_test

import (
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

func TestRatString(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		rat  *big.Rat
		want string
	}{
		"integer":          {rat: big.NewRat(5, 1), want: "5"},
		"negative integer": {rat: big.NewRat(-7, 1), want: "-7"},
		"fraction":         {rat: big.NewRat(1, 4), want: "0.25"},
		"non-exact tenth":  {rat: big.NewRat(1, 10), want: "0.1"},
		"non-exact 0.3":    {rat: big.NewRat(3, 10), want: "0.3"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, numrat.RatString(tc.rat))
		})
	}
}

func TestRatStringFallsBackToExactForm(t *testing.T) {
	t.Parallel()

	huge := new(big.Int).Exp(big.NewInt(10), big.NewInt(400), nil)

	// A magnitude above the float64 range overflows to +Inf, and a magnitude
	// below the smallest subnormal underflows to 0; both keep the exact form.
	overflow := numrat.RatString(new(big.Rat).SetFrac(huge, big.NewInt(3)))
	assert.Contains(t, overflow, "/")

	underflow := numrat.RatString(new(big.Rat).SetFrac(big.NewInt(1), huge))
	assert.Equal(t, "1/"+huge.String(), underflow)

	// A finite, in-range fraction with more precision than a float64 holds keeps
	// the exact form instead of reporting the rounded shortest-float value.
	precise, ok := new(big.Rat).SetString("1.000000000000000000000000000001")
	assert.True(t, ok)
	assert.Equal(t, "1000000000000000000000000000001/1000000000000000000000000000000",
		numrat.RatString(precise))
}

func TestTruncateNumber(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   string
		want string
	}{
		"short":      {in: "123", want: "123"},
		"exactly 32": {in: strings.Repeat("9", 32), want: strings.Repeat("9", 32)},
		"truncated":  {in: strings.Repeat("9", 50), want: strings.Repeat("9", 32) + "... (50 chars)"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, numrat.TruncateNumber(tc.in))
		})
	}
}
