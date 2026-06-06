package jsonschema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"go.jacobcolvin.com/x/jsonschema"
)

// BenchmarkValidateLargeNumber exercises validation of multi-megabyte JSON
// number literals. The guarded paths classify such values in a single O(n)
// scan; a regression into an unguarded big.Rat parse (quadratic in the digit
// count) shows up here as a roughly thousandfold ns/op jump.
func BenchmarkValidateLargeNumber(b *testing.B) {
	big := strings.Repeat("9", 5_000_000)

	cases := map[string]struct {
		schema   string
		instance string
	}{
		"giant integer exceeds maximum": {`{"type":"integer","maximum":100}`, big},
		"unique giant literals":         {`{"uniqueItems":true}`, "[" + big + "," + big + "]"},
	}

	for name, c := range cases {
		b.Run(name, func(b *testing.B) {
			var s jsonschema.Schema

			err := json.Unmarshal([]byte(c.schema), &s)
			if err != nil {
				b.Fatal(err)
			}

			v, err := jsonschema.Compile(&s)
			if err != nil {
				b.Fatal(err)
			}

			instance := []byte(c.instance)

			b.SetBytes(int64(len(instance)))
			b.ResetTimer()

			// Both cases validate an instance that violates the schema, so a
			// nil error means the benchmark stopped measuring the real path.
			for b.Loop() {
				if v.ValidateJSON(instance) == nil {
					b.Fatal("expected a validation error")
				}
			}
		})
	}
}
