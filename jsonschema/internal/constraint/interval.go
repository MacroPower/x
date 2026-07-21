package constraint

import "math/big"

// Endpoint is one side of an [Interval]: a rational limit and its strictness. A
// nil Rat means the side is unbounded. Val carries the float64 the numeric
// keyword renders as, kept beside Rat so exact comparison and a faithful
// round-trip both work; it is unused in the integer size domain, where Rat holds
// an integer rational and the keyword renders as an int taken from Rat directly.
type Endpoint struct {
	Rat       *big.Rat
	Val       float64
	Inclusive bool
}

// set reports whether the endpoint bounds its side (a non-nil limit).
func (e Endpoint) set() bool { return e.Rat != nil }

// numericEndpoint builds an inclusive-by-default numeric endpoint from a float64
// whose rational form is its shortest decimal, so 0.1 compares as 1/10.
func numericEndpoint(val float64, rat *big.Rat) Endpoint {
	return Endpoint{Rat: rat, Val: val, Inclusive: true}
}

// intEndpoint builds an inclusive integer endpoint for the size domain.
func intEndpoint(n int) Endpoint {
	return Endpoint{Rat: new(big.Rat).SetInt64(int64(n)), Inclusive: true}
}

// Interval is a range over the rationals, optionally restricted to the integers
// and/or the non-negative reals. A zero Interval is unbounded on both sides.
type Interval struct {
	Lo, Hi      Endpoint
	Integral    bool // domain is the integers (length/count keywords, integer kinds)
	NonNegative bool // floor clamps at 0 (length/count keywords)
}

// Intersect returns the tighter of each side, so the result is the conjunction
// of the two ranges. It is commutative and associative, which is what makes a
// merge built from it order-independent.
func (iv Interval) Intersect(o Interval) Interval {
	return Interval{
		Lo:          tighter(true, iv.Lo, o.Lo),
		Hi:          tighter(false, iv.Hi, o.Hi),
		Integral:    iv.Integral || o.Integral,
		NonNegative: iv.NonNegative || o.NonNegative,
	}
}

// Empty reports whether no value satisfies the interval: the floor lies above
// the ceiling, the two coincide with either side exclusive, or the integral
// domain has no integer between them. An empty interval is still rendered as its
// impossible bounds rather than loosened, so this is a report, not a gate.
func (iv Interval) Empty() bool {
	if !iv.Lo.set() || !iv.Hi.set() {
		return false
	}

	switch c := iv.Lo.Rat.Cmp(iv.Hi.Rat); {
	case c > 0:
		return true
	case c == 0:
		return !iv.Lo.Inclusive || !iv.Hi.Inclusive
	default:
		if iv.Integral {
			return ceilLower(iv.Lo).Cmp(floorUpper(iv.Hi)) > 0
		}

		return false
	}
}

// Point returns the single value an interval pins, when both ends are set,
// equal, and inclusive.
func (iv Interval) Point() (*big.Rat, bool) {
	if iv.Lo.set() && iv.Hi.set() && iv.Lo.Inclusive && iv.Hi.Inclusive &&
		iv.Lo.Rat.Cmp(iv.Hi.Rat) == 0 {
		return iv.Lo.Rat, true
	}

	return nil, false
}

// tighter returns the stronger of two endpoints on one side. An unset endpoint
// yields the other. On a tie the exclusive endpoint wins, since it admits fewer
// values. Lower selects the direction: a larger floor or a smaller ceiling is
// tighter.
func tighter(lower bool, a, b Endpoint) Endpoint {
	if !a.set() {
		return b
	}

	if !b.set() {
		return a
	}

	c := a.Rat.Cmp(b.Rat)
	if !lower {
		c = -c
	}

	switch {
	case c > 0:
		return a
	case c < 0:
		return b
	case !a.Inclusive:
		return a
	default:
		return b
	}
}

// ceilLower returns the smallest integer the lower endpoint admits.
func ceilLower(e Endpoint) *big.Int {
	q := new(big.Int)
	m := new(big.Int)
	q.DivMod(e.Rat.Num(), e.Rat.Denom(), m) // floor division, m >= 0

	if m.Sign() != 0 {
		// Not an integer: the ceiling is floor+1 whether or not the bound is
		// exclusive.
		return q.Add(q, big.NewInt(1))
	}

	if !e.Inclusive {
		return q.Add(q, big.NewInt(1))
	}

	return q
}

// floorUpper returns the largest integer the upper endpoint admits.
func floorUpper(e Endpoint) *big.Int {
	q := new(big.Int)
	m := new(big.Int)
	q.DivMod(e.Rat.Num(), e.Rat.Denom(), m) // floor division, m >= 0

	if m.Sign() == 0 && !e.Inclusive {
		// An exclusive integer ceiling admits up to one below it.
		return q.Sub(q, big.NewInt(1))
	}

	return q
}
