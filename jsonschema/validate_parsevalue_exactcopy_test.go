package jsonschema_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
)

// assertParseValueMatchesRemarshal pins ParseSchemaValue's direct exact copy
// of value members to the marshal round trip it replaced: parsing doc
// directly must produce the same schema as marshaling doc and parsing the
// bytes, whose value members take the exact-decode path by construction.
func assertParseValueMatchesRemarshal(t *testing.T, doc any) {
	t.Helper()

	data, err := json.Marshal(doc)
	require.NoError(t, err)

	want, wantErr := jsonschema.ParseSchema(data)
	got, gotErr := jsonschema.ParseSchemaValue(doc)

	if wantErr != nil {
		require.Error(t, gotErr)

		return
	}

	require.NoError(t, gotErr)
	assert.Equal(t, want, got)
}

// TestParseSchemaValueExactCopyMatchesRemarshal covers Go-typed leaves in
// every member restoreExactValues repairs (const, enum, examples, and unknown
// keywords), including the shapes that exercise the exact copy's marshal
// fallback.
func TestParseSchemaValueExactCopyMatchesRemarshal(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc any
	}{
		"const float64 fraction": {doc: map[string]any{"const": 0.1}},
		"const float32":          {doc: map[string]any{"const": float32(0.1)}},
		"const large uint64":     {doc: map[string]any{"const": uint64(1)<<60 + 1}},
		"const number literal":   {doc: map[string]any{"const": jsonv1.Number("9007199254740993")}},
		"enum mixed leaves": {doc: map[string]any{
			"enum": []any{1, 2.5, jsonv1.Number("9007199254740993"), "s", nil, true},
		}},
		"examples nested containers": {doc: map[string]any{
			"examples": []any{map[string]any{"n": uint64(1)<<60 + 1, "f": float32(2.5)}},
		}},
		"unknown keyword subschema": {doc: map[string]any{
			"myext": map[string]any{"const": jsonv1.Number("9007199254740993")},
		}},
		"unknown keyword struct fallback": {doc: map[string]any{
			"myext": struct {
				N int `json:"n"`
			}{N: 1},
		}},
		"unknown keyword raw message fallback": {doc: map[string]any{
			"myext": jsonv1.RawMessage(`{"n":9007199254740993}`),
		}},
		"unknown keyword raw value fallback": {doc: map[string]any{
			"myext": jsontext.Value(`[1,2]`),
		}},
		"multipleOf underflow literal": {doc: map[string]any{
			"multipleOf": jsonv1.Number("1e-320"),
		}},
		"boolean document": {doc: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assertParseValueMatchesRemarshal(t, tt.doc)
		})
	}
}
