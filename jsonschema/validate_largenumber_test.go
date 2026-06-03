package jsonschema_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
)

// TestValidateLargeNumberGuarded covers validation of a multi-megabyte JSON
// number: it must stay fast (big.Rat parsing is quadratic in the digit count)
// while still producing correct results.
func TestValidateLargeNumberGuarded(t *testing.T) {
	t.Parallel()

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
			// A generous bound: the guarded path runs in tens of milliseconds,
			// whereas an unguarded big.Rat parse of this input takes ~25 seconds.
			assert.Less(t, time.Since(start), 5*time.Second)

			if c.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
