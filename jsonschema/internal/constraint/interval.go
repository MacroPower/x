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

// Interval is a range over the rationals. A zero Interval is unbounded on both
// sides. An unsatisfiable interval (a floor above its ceiling) is representable
// and rendered as its impossible bounds rather than loosened, so an
// over-constrained field rejects every instance instead of silently accepting
// some.
type Interval struct {
	Lo, Hi Endpoint
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
