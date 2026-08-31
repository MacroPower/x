package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// bothMarshalers directly implements both encoding.TextMarshaler and
// json.Marshaler. Since encoding/json prefers MarshalJSON over MarshalText,
// the text form never appears in the output; the generator must not claim
// {"type":"string"} for it and instead falls through to kind-based
// reflection per the documented resolution priority, just like any other
// direct json.Marshaler.
type bothMarshalers int

func (bothMarshalers) MarshalJSON() ([]byte, error) { return []byte("42"), nil }

func (bothMarshalers) MarshalText() ([]byte, error) { return []byte("forty-two"), nil }

func TestGenerateFor_DirectTextAndJSONMarshalerFallsThrough(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[bothMarshalers](t.Context())
	require.NoError(t, err)

	// Kind-based reflection over the int kind, not the TextMarshaler string
	// claim: a string schema would reject every actual serialization.
	assert.Equal(t, "integer", s.Type, "schema: %s", marshalSchema(t, s))

	data, err := json.Marshal(bothMarshalers(7))
	require.NoError(t, err)
	require.Equal(t, "42", string(data))

	assert.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the type's actual serialization: %s", data)
}
