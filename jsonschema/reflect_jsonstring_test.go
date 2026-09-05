package jsonschema_test

import (
	"encoding/json/v2"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// intPtr is a named pointer type; encoding/json/v2's json:",string" flag
// survives every pointer level, named or not, so every pointer chain over a
// numeric payload is quoted.
type intPtr *int

func TestGenerateFor_JSONStringPointerKinds(t *testing.T) {
	t.Parallel()

	type doc struct {
		A **int  `json:",string"` //nolint:staticcheck // SA5008 models v1; v2 carries the flag down the whole pointer chain.
		B intPtr `json:",string"`
		C *int   `json:",string"`
		D int    `json:",string"`
	}

	// Encoding/json/v2 quotes every chain, the double pointer included (v1
	// dereferenced exactly one level and left A bare).
	v := 5
	p := &v
	data, err := json.Marshal(doc{A: &p, B: intPtr(&v), C: &v, D: 5})
	require.NoError(t, err)
	require.JSONEq(t, `{"A":"5","B":"5","C":"5","D":"5"}`, string(data))

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"A":{"type":["null","string"]},
			"B":{"type":["null","string"]},
			"C":{"type":["null","string"]},
			"D":{"type":"string"}
		},
		"required":["A","B","C","D"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)

	nilData, err := json.Marshal(doc{D: 5})
	require.NoError(t, err)
	require.JSONEq(t, `{"A":null,"B":null,"C":null,"D":"5"}`, string(nilData))
	assert.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-pointer serialization: %s", nilData)
}

// TestGenerateFor_JSONStringDurationRefused pins that the json:",string"
// override never claims a time.Duration. Encoding/json/v2's duration codec
// has no default representation and refuses the value before it consults the
// flag, so the field takes the same path as an unflagged duration: refused
// under the defaults, and answered by a type override when one is registered.
func TestGenerateFor_JSONStringDurationRefused(t *testing.T) {
	t.Parallel()

	type value struct {
		D time.Duration `json:"d,string"`
	}

	type pointer struct {
		D *time.Duration `json:"d,string"`
	}

	override := jsonschema.WithTypeSchemaFor[time.Duration](jsonschema.TypeSchema{
		Value: &jsonschema.Schema{Type: "string", Pattern: "^[0-9]+s$"},
	})

	t.Run("value refused", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[value](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
	})

	t.Run("pointer refused", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[pointer](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
	})

	t.Run("override applies", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[value](t.Context(), override)
		require.NoError(t, err)
		assert.Equal(t, "^[0-9]+s$", s.Properties["d"].Pattern)
	})
}

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

// quotedJSONMarshaler is an int-kind type that directly implements
// json.Marshaler. Encoding/json/v2 routes a marshaler-bearing field through
// its method, which ignores the json:",string" option, so the generator must
// not coerce such a field to {"type":"string"}.
type quotedJSONMarshaler int

func (quotedJSONMarshaler) MarshalJSON() ([]byte, error) { return []byte("7"), nil }

// quotedPtrJSONMarshaler carries MarshalJSON on the pointer receiver only; the
// addressable-field encoding still routes through it, so ",string" is ignored
// for it too.
type quotedPtrJSONMarshaler int

func (*quotedPtrJSONMarshaler) MarshalJSON() ([]byte, error) { return []byte("8"), nil }

func TestGenerateFor_JSONStringMarshalerKeepsKindSchema(t *testing.T) {
	t.Parallel()

	type doc struct {
		F quotedJSONMarshaler    `json:"f,string"`
		G *quotedJSONMarshaler   `json:"g,string"`
		P quotedPtrJSONMarshaler `json:"p,string"`
	}

	// Encoding/json/v2 routes f, g, and p through MarshalJSON, discarding the
	// quoted option.
	v := quotedJSONMarshaler(3)
	data, err := json.Marshal(&doc{F: 3, G: &v, P: 4})
	require.NoError(t, err)
	require.JSONEq(t, `{"f":7,"g":7,"p":8}`, string(data))

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The marshaler fields keep the kind-based integer schema a direct
	// json.Marshaler gets instead of the string coercion its output never
	// satisfies.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"f":{"type":"integer"},
			"g":{"anyOf":[{"type":"integer"},{"type":"null"}]},
			"p":{"type":"integer"}
		},
		"required":["f","g","p"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)
}
