package jsonschema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateLargeNumberGuarded covers correctness of the guarded numeric
// paths for literals past the internal precision cap (~4096 significant
// digits or decimal exponent magnitude): magnitude classification, exact
// bound comparison, const/enum equality, and uniqueItems deduplication all
// avoid materializing the value as a big.Rat. The performance property (the
// guard avoids big.Rat's quadratic parse) is covered by
// BenchmarkValidateLargeNumber.
func TestValidateLargeNumberGuarded(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("9", 5_000)

	cases := map[string]struct {
		schema   string
		instance string
		valid    bool
	}{
		"giant integer is an integer":   {`{"type":"integer"}`, big, true},
		"giant integer exceeds maximum": {`{"type":"integer","maximum":100}`, big, false},
		"giant negative below minimum":  {`{"type":"integer","minimum":0}`, "-" + big, false},
		"exact comparison within range": {`{"maximum":9007199254740992}`, "9007199254740993", false},

		// A short literal with a large exponent expands to a huge value;
		// big.Rat.SetString would materialize the full integer, so the guard
		// must classify it by magnitude without expanding it.
		"large exponent is an integer":      {`{"type":"integer"}`, "1e5000", true},
		"large exponent exceeds maximum":    {`{"type":"integer","maximum":100}`, "1e5000", false},
		"large exponent above minimum":      {`{"minimum":1}`, "1e5000", true},
		"negative large exponent magnitude": {`{"minimum":0}`, "-1e5000", false},

		// The multipleOf check is documented as skipped for values past the
		// cap, so any unbounded value passes regardless of the divisor.
		"multipleOf skipped for large exponent": {`{"multipleOf":3}`, "1e5000", true},

		// Const and enum compare via equality rather than the numeric bound
		// path; a giant literal must not reach an unguarded big.Rat parse.
		"giant literal never matches const": {`{"const":1}`, big, false},
		"giant literal never matches enum":  {`{"enum":[1,2]}`, big, false},

		// UniqueItems hashes and compares array members; large-exponent
		// members must be deduplicated canonically without expansion.
		"unique giant exponents distinct":  {`{"uniqueItems":true}`, "[1e5000,2e5000,3e5000]", true},
		"unique giant exponents duplicate": {`{"uniqueItems":true}`, "[1e5000,10e4999]", false},
		"unique giant literals duplicate":  {`{"uniqueItems":true}`, "[" + big + "," + big + "]", false},

		// An over-length literal whose value is small must compare by value,
		// not be misclassified as extreme by its textual length.
		"long small literal within maximum": {`{"maximum":100}`, "1." + strings.Repeat("0", 5000), true},
		"long small negative above minimum": {`{"minimum":-100}`, "-1." + strings.Repeat("0", 5000), true},

		// A tiny magnitude (large negative exponent) sits strictly between
		// zero and every nonzero bound on its side.
		"tiny positive above minimum zero":          {`{"minimum":0}`, "1e-5000", true},
		"tiny positive below maximum zero":          {`{"maximum":0}`, "1e-5000", false},
		"tiny positive above exclusiveMinimum zero": {`{"exclusiveMinimum":0}`, "1e-5000", true},
		"tiny positive not an integer":              {`{"type":"integer"}`, "1e-5000", false},

		// More significant digits than any float64 expansion: ordering is
		// decided by the truncated significand, equality is impossible.
		"overprecise small below maximum": {`{"maximum":100}`, "0." + strings.Repeat("123456789", 600), true},
		"overprecise never matches const": {`{"const":0.5}`, "0." + strings.Repeat("123456789", 600), false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var s jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(c.schema), &s))

			v, err := jsonschema.Compile(t.Context(), &s)
			require.NoError(t, err)

			err = v.ValidateJSON(t.Context(), []byte(c.instance))

			if c.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
