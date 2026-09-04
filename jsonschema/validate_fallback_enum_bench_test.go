package jsonschema_test

import (
	"encoding/json/v2"
	"fmt"
	"testing"

	"go.jacobcolvin.com/x/jsonschema"
)

// BenchmarkValidateFallbackEnum validates a 10,000-element array of strings
// against a 50-member string enum, each element matching the last member so
// every evaluation scans the whole enum. The "indexed" variant holds the enum
// under items, where Compile caches its document view by node id. The
// "fallback" variant reaches the same enum through a JSON-pointer ref into an
// unknown keyword (#/examples/0), which a run materializes as a fresh schema
// outside the index, so the enum's document view comes from the run's memo
// rather than the compile-time cache. The ref resolution itself costs the
// fallback variant about two allocations per element over the indexed one;
// a view rebuilt per element adds one more allocation and the 50-member
// conversion (about 900 bytes) on top, so a gap much wider than that means
// the memo stopped holding.
func BenchmarkValidateFallbackEnum(b *testing.B) {
	const (
		enumSize  = 50
		arraySize = 10000
	)

	members := make([]string, enumSize)
	for i := range members {
		members[i] = fmt.Sprintf("member-%02d", i)
	}

	enum, err := json.Marshal(members)
	if err != nil {
		b.Fatal(err)
	}

	elements := make([]string, arraySize)
	for i := range elements {
		elements[i] = members[enumSize-1]
	}

	instance, err := json.Marshal(elements)
	if err != nil {
		b.Fatal(err)
	}

	schemas := map[string]string{
		"indexed": `{"type": "array", "items": {"enum": ` + string(enum) + `}}`,
		"fallback": `{"type": "array", "items": {"$ref": "#/examples/0"}, ` +
			`"examples": [{"enum": ` + string(enum) + `}]}`,
	}

	for name, schema := range schemas {
		b.Run(name, func(b *testing.B) {
			v, err := jsonschema.CompileJSON(b.Context(), []byte(schema))
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()

			// Every element matches, so a non-nil error means the benchmark
			// stopped measuring the full scan.
			for b.Loop() {
				if v.ValidateJSON(b.Context(), instance) != nil {
					b.Fatal("expected the instance to validate")
				}
			}
		})
	}
}
