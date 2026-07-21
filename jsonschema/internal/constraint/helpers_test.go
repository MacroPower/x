package constraint_test

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

// incl builds an inclusive endpoint at integer n.
func incl(n int64) constraint.Endpoint {
	return constraint.Endpoint{Rat: big.NewRat(n, 1), Val: float64(n), Inclusive: true}
}

// excl builds an exclusive endpoint at integer n.
func excl(n int64) constraint.Endpoint {
	return constraint.Endpoint{Rat: big.NewRat(n, 1), Val: float64(n), Inclusive: false}
}

// exclBig builds an exclusive endpoint from a decimal literal too large for an
// int64, e.g. the int64 kind's exclusive maximum of 2^63.
func exclBig(s string) constraint.Endpoint {
	r, _ := new(big.Rat).SetString(s)
	f, _ := r.Float64()

	return constraint.Endpoint{Rat: r, Val: f, Inclusive: false}
}

func lowerBound(e constraint.Endpoint, mode constraint.Mode, prov constraint.Provenance) constraint.Bound {
	return constraint.Bound{End: e, Lower: true, Mode: mode, Provenance: prov}
}

func upperBound(e constraint.Endpoint, mode constraint.Mode, prov constraint.Provenance) constraint.Bound {
	return constraint.Bound{End: e, Lower: false, Mode: mode, Provenance: prov}
}

// authoredLower and authoredUpper build the common validate-tag intersect bound.
func authoredLower(e constraint.Endpoint) constraint.Bound {
	return lowerBound(e, constraint.Intersect, constraint.Authored)
}

func authoredUpper(e constraint.Endpoint) constraint.Bound {
	return upperBound(e, constraint.Intersect, constraint.Authored)
}

func assertEndpointEqual(t *testing.T, want, got constraint.Endpoint) {
	t.Helper()

	if want.Rat == nil {
		assert.Nil(t, got.Rat, "expected an unset endpoint")

		return
	}

	require.NotNil(t, got.Rat, "expected a set endpoint")
	assert.Equal(t, 0, want.Rat.Cmp(got.Rat), "rational mismatch: want %s got %s", want.Rat, got.Rat)
	assert.Equal(t, want.Inclusive, got.Inclusive, "inclusivity mismatch")
}
