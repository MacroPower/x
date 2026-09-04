package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv2 "encoding/json/v2"

	"go.jacobcolvin.com/x/jsonschema"
)

// numberOmitEmpty holds the one string-kind field omitempty never omits:
// json.Number's MarshalJSONTo writes 0 for the empty value, so encoding/json/v2
// always encodes it and the field stays required. A pointer to it is omitted
// when nil, as any pointer is, and omitzero omits the zero Number.
type numberOmitEmpty struct {
	N  json.Number  `json:"n,omitempty"`
	NP *json.Number `json:"np,omitempty"`
	NZ json.Number  `json:"nz,omitzero"`
}

func TestGenerateFor_JSONNumberOmitEmptyStaysRequired(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[numberOmitEmpty](t.Context())
	require.NoError(t, err)

	assert.Equal(t, []string{"n"}, s.Required)

	// Oracle: v2 keeps n even for the zero value, so the marshaled zero value
	// must validate and a document without n must not.
	out, err := jsonv2.Marshal(numberOmitEmpty{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":0}`, string(out))
	require.NoError(t, validateJSON(t.Context(), s, out))
	require.Error(t, validateJSON(t.Context(), s, []byte(`{}`)))
}
