package jsonschema_test

import (
	"encoding/json"
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// bigFloatInfDoc carries a big.Float by pointer so its pointer-receiver
// MarshalText always runs, isolating the pattern under test from
// addressability concerns.
type bigFloatInfDoc struct {
	V *big.Float `json:"v"`
}

// TestGenerateFor_BigFloatInfinityRoundTrip pins that the big.Float built-in
// override's pattern admits every text big.Float legitimately marshals:
// big.Float can hold infinities (only NaN is unrepresentable), and
// MarshalText emits "+Inf"/"-Inf" for them without error, so the generated
// schema must accept that output alongside finite decimal forms.
func TestGenerateFor_BigFloatInfinityRoundTrip(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value *big.Float
		want  string
	}{
		"positive infinity": {value: big.NewFloat(math.Inf(1)), want: `{"v":"+Inf"}`},
		"negative infinity": {value: big.NewFloat(math.Inf(-1)), want: `{"v":"-Inf"}`},
		"finite":            {value: big.NewFloat(1.5), want: `{"v":"1.5"}`},
	}

	s, err := jsonschema.GenerateFor[bigFloatInfDoc](t.Context())
	require.NoError(t, err)

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(bigFloatInfDoc{V: tc.value})
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(data))

			assert.NoError(t, validateJSON(t.Context(), s, data),
				"generated schema rejected big.Float's actual serialization: %s", data)
		})
	}
}

// TestGenerateFor_BigFloatPatternRejectsUnsignedInf pins that widening the
// pattern for infinities stays anchored: big.Float never marshals a bare
// "Inf" (the sign is always present), so the schema still rejects it.
func TestGenerateFor_BigFloatPatternRejectsUnsignedInf(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[bigFloatInfDoc](t.Context())
	require.NoError(t, err)

	assert.Error(t, validateJSON(t.Context(), s, []byte(`{"v":"Inf"}`)))
}
