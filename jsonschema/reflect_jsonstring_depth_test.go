package jsonschema_test

import (
	"encoding/json/v2"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// pointerChain wraps elem in depth pointer levels.
func pointerChain(elem reflect.Type, depth int) reflect.Type {
	for range depth {
		elem = reflect.PointerTo(elem)
	}

	return elem
}

// stringField builds the one-field struct type whose F is typ under
// json:",string".
func stringField(typ reflect.Type) reflect.Type {
	return reflect.StructOf([]reflect.StructField{{
		Name: "F",
		Type: typ,
		Tag:  `json:",string"`,
	}})
}

// allocated returns a value of typ with every pointer level allocated and
// the leaf int set to 42.
func allocated(typ reflect.Type) reflect.Value {
	if typ.Kind() != reflect.Pointer {
		return reflect.ValueOf(42).Convert(typ)
	}

	p := reflect.New(typ.Elem())
	p.Elem().Set(allocated(typ.Elem()))

	return p
}

// TestGenerateJSONStringAtEveryPointerDepth pins that the json:",string"
// override reaches an integer leaf behind any number of pointer levels.
// Encoding/json/v2 carries the flag down the whole chain, so the schema
// must read ["null","string"] at every depth and accept what v2 writes. The
// table runs past the probe's former fill cap of five, where a nil pointer
// once marshaled as null and the field reported as an integer.
func TestGenerateJSONStringAtEveryPointerDepth(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		depth int
	}{
		"one":   {depth: 1},
		"two":   {depth: 2},
		"three": {depth: 3},
		"four":  {depth: 4},
		"five":  {depth: 5},
		"six":   {depth: 6},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typ := stringField(pointerChain(reflect.TypeFor[int](), tc.depth))

			val := reflect.New(typ).Elem()
			val.Field(0).Set(allocated(typ.Field(0).Type))

			data, err := json.Marshal(val.Interface())
			require.NoError(t, err)
			require.JSONEq(t, `{"F":"42"}`, string(data),
				"encoding/json/v2 must quote the leaf at depth %d", tc.depth)

			s, err := jsonschema.Generate(t.Context(), typ)
			require.NoError(t, err)

			want := &jsonschema.Schema{Types: []string{"null", "string"}}
			assert.Equal(t, want, s.Properties["F"])

			require.NoError(t, validateJSON(t.Context(), s, data),
				"generated schema rejected the struct's own serialization: %s", data)
		})
	}
}

// TestGenerateRefusesLeafBehindSixPointers pins that a kind v2 cannot encode
// is refused however many pointer levels hide it. A fill that stopped short
// of the leaf would leave a nil pointer marshaling as null, and the refusal
// would read as an accepted nullable field.
func TestGenerateRefusesLeafBehindSixPointers(t *testing.T) {
	t.Parallel()

	typ := reflect.StructOf([]reflect.StructField{{
		Name: "F",
		Type: pointerChain(reflect.TypeFor[func()](), 6),
		Tag:  `json:"f"`,
	}})

	_, err := jsonschema.Generate(t.Context(), typ)
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
}
