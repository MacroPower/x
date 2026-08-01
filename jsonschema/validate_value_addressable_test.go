package jsonschema_test

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// bigValueDoc holds big.Int and big.Float by value. Their MarshalJSON and
// MarshalText have pointer receivers, which encoding/json only uses on
// addressable values: a bare json.Marshal of the value form would fall back
// to struct reflection ({"i":{},"v":{}}), a shape generation never
// describes. ValidateValue must marshal through an addressable copy so the
// value instance validates identically to the pointer instance, closing the
// generation loop for both forms.
type bigValueDoc struct {
	I big.Int   `json:"i"`
	V big.Float `json:"v"`
}

func TestValidateValue_NonAddressableFieldMarshalers(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[bigValueDoc](t.Context())
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	doc := bigValueDoc{I: *big.NewInt(42), V: *big.NewFloat(1.5)}

	assert.NoError(t, v.ValidateValue(t.Context(), doc),
		"value instance must validate like the pointer instance")
	assert.NoError(t, v.ValidateValue(t.Context(), &doc))
}

func TestValidateValue_NonAddressableRootMarshaler(t *testing.T) {
	t.Parallel()

	// The root itself relies on a pointer-receiver marshaler: big.Int's
	// MarshalJSON emits a bare number, and the generated schema is
	// {"type":"integer"}, which the reflected {} of a non-addressable
	// marshal would fail.
	s, err := jsonschema.GenerateFor[big.Int](t.Context())
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	assert.NoError(t, v.ValidateValue(t.Context(), *big.NewInt(7)))
	assert.NoError(t, v.ValidateValue(t.Context(), big.NewInt(7)))
}
