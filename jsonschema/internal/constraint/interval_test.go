package constraint_test

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

func TestIntervalIntersect(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		a, b   constraint.Interval
		wantLo constraint.Endpoint
		wantHi constraint.Endpoint
	}{
		"tighter floor wins": {
			a:      constraint.Interval{Lo: incl(1)},
			b:      constraint.Interval{Lo: incl(5)},
			wantLo: incl(5),
		},
		"tighter ceiling wins": {
			a:      constraint.Interval{Hi: incl(10)},
			b:      constraint.Interval{Hi: incl(3)},
			wantHi: incl(3),
		},
		"exclusive wins tie on floor": {
			a:      constraint.Interval{Lo: incl(5)},
			b:      constraint.Interval{Lo: excl(5)},
			wantLo: excl(5),
		},
		"exclusive wins tie on ceiling": {
			a:      constraint.Interval{Hi: incl(5)},
			b:      constraint.Interval{Hi: excl(5)},
			wantHi: excl(5),
		},
		"unset side yields the other": {
			a:      constraint.Interval{Lo: incl(1), Hi: incl(9)},
			b:      constraint.Interval{Lo: incl(4)},
			wantLo: incl(4),
			wantHi: incl(9),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := tc.a.Intersect(tc.b)
			assertEndpointEqual(t, tc.wantLo, got.Lo)
			assertEndpointEqual(t, tc.wantHi, got.Hi)

			// Intersect is commutative.
			rev := tc.b.Intersect(tc.a)
			assertEndpointEqual(t, got.Lo, rev.Lo)
			assertEndpointEqual(t, got.Hi, rev.Hi)
		})
	}
}

func TestIntervalEmpty(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		iv   constraint.Interval
		want bool
	}{
		"floor above ceiling":          {iv: constraint.Interval{Lo: incl(5), Hi: incl(3)}, want: true},
		"equal inclusive is a point":   {iv: constraint.Interval{Lo: incl(5), Hi: incl(5)}, want: false},
		"equal with exclusive floor":   {iv: constraint.Interval{Lo: excl(5), Hi: incl(5)}, want: true},
		"equal with exclusive ceiling": {iv: constraint.Interval{Lo: incl(5), Hi: excl(5)}, want: true},
		"open range not empty":         {iv: constraint.Interval{Lo: incl(1), Hi: incl(9)}, want: false},
		"unbounded never empty":        {iv: constraint.Interval{Lo: incl(9)}, want: false},
		"integral gap between exclusive": {
			iv:   constraint.Interval{Lo: excl(2), Hi: excl(3), Integral: true},
			want: true,
		},
		"integral admits one": {
			iv:   constraint.Interval{Lo: incl(2), Hi: incl(3), Integral: true},
			want: false,
		},
		"unsat size floor1 ceiling0": {
			iv:   constraint.Interval{Lo: incl(1), Hi: incl(0), Integral: true, NonNegative: true},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.iv.Empty())
		})
	}
}

func TestIntervalPoint(t *testing.T) {
	t.Parallel()

	got, ok := constraint.Interval{Lo: incl(7), Hi: incl(7)}.Point()
	require.True(t, ok)
	assert.Equal(t, 0, got.Cmp(big.NewRat(7, 1)))

	_, ok = constraint.Interval{Lo: incl(7), Hi: excl(7)}.Point()
	assert.False(t, ok)

	_, ok = constraint.Interval{Lo: incl(7)}.Point()
	assert.False(t, ok)
}
