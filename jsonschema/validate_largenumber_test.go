package jsonschema_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateLargeNumberGuarded covers validation of a multi-megabyte JSON
// number: it must stay fast (big.Rat parsing is quadratic in the digit count)
// while still producing correct results.
func TestValidateLargeNumberGuarded(t *testing.T) {
	t.Parallel()

	// The guarded path runs in tens of milliseconds, but coverage
	// instrumentation counts every statement in the digit-by-digit scans of
	// these multi-megabyte literals and inflates that to ~20s. The timing
	// bound exists to catch a regression that drops into an unguarded
	// big.Rat parse (~25s uninstrumented, far worse instrumented), so relax
	// it under coverage while keeping it tight for ordinary runs.
	bound := 5 * time.Second
	if testing.CoverMode() != "" {
		bound = 60 * time.Second
	}

	big := strings.Repeat("9", 5_000_000)

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
		// big.Rat.SetString would materialize the full ~1MB integer, so the
		// guard must classify it by magnitude without expanding it.
		"large exponent is an integer":      {`{"type":"integer"}`, "1e1000000", true},
		"large exponent exceeds maximum":    {`{"type":"integer","maximum":100}`, "1e1000000", false},
		"large exponent above minimum":      {`{"minimum":1}`, "1e1000000", true},
		"negative large exponent magnitude": {`{"minimum":0}`, "-1e1000000", false},

		// Const and enum compare via equality rather than the numeric bound
		// path; a giant literal must not reach an unguarded big.Rat parse.
		"giant literal never matches const": {`{"const":1}`, big, false},
		"giant literal never matches enum":  {`{"enum":[1,2]}`, big, false},

		// UniqueItems hashes and compares array members; large-exponent
		// members must be deduplicated canonically without expansion.
		"unique giant exponents distinct":  {`{"uniqueItems":true}`, "[1e1000000,2e1000000,3e1000000]", true},
		"unique giant exponents duplicate": {`{"uniqueItems":true}`, "[1e1000000,10e999999]", false},
		"unique giant literals duplicate":  {`{"uniqueItems":true}`, "[" + big + "," + big + "]", false},

		// An over-length literal whose value is small must compare by value,
		// not be misclassified as extreme by its textual length.
		"long small literal within maximum": {`{"maximum":100}`, "1." + strings.Repeat("0", 5000), true},
		"long small negative above minimum": {`{"minimum":-100}`, "-1." + strings.Repeat("0", 5000), true},

		// A tiny magnitude (large negative exponent) sits strictly between
		// zero and every nonzero bound on its side.
		"tiny positive above minimum zero":          {`{"minimum":0}`, "1e-1000000", true},
		"tiny positive below maximum zero":          {`{"maximum":0}`, "1e-1000000", false},
		"tiny positive above exclusiveMinimum zero": {`{"exclusiveMinimum":0}`, "1e-1000000", true},
		"tiny positive not an integer":              {`{"type":"integer"}`, "1e-1000000", false},

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

			v, err := jsonschema.Compile(&s)
			require.NoError(t, err)

			start := time.Now()
			err = v.ValidateJSON([]byte(c.instance))

			assert.Less(t, time.Since(start), bound)

			if c.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
