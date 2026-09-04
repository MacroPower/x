package jsonschema_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv2 "encoding/json/v2"

	"go.jacobcolvin.com/x/jsonschema"
)

// textAlways is an integer kind whose text marshaler writes a non-empty
// string for every value, the zero included.
type textAlways int

func (textAlways) MarshalText() ([]byte, error) { return []byte("always"), nil }

// jsonEmpty is an integer kind whose JSON marshaler writes the empty string,
// which omitempty drops.
type jsonEmpty int

func (jsonEmpty) MarshalJSON() ([]byte, error) { return []byte(`""`), nil }

// TestGenerateOmitEmptyPresence pins which omitempty fields stay required.
// Encoding/json/v2 omits a member only when its encoded value is empty, and
// the zero value is the emptiest value a type encodes, so each row marshals
// the zero value as the oracle for what v2 writes. A marshaler-bearing type
// is the one row where the schema is looser than v2: the generator never
// runs user methods, so it leaves the field optional whatever the method
// writes, and the schema still accepts every document v2 produces.
func TestGenerateOmitEmptyPresence(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ      reflect.Type
		wantDoc  string
		required []string
	}{
		"time": {
			typ: reflect.TypeFor[struct {
				T time.Time `json:"t,omitempty"` //nolint:modernize // Whether v2 omits a struct under omitempty is what the row pins.
			}](),
			wantDoc:  `{"t":"0001-01-01T00:00:00Z"}`,
			required: []string{"t"},
		},
		"number": {
			typ: reflect.TypeFor[struct {
				N json.Number `json:"n,omitempty"`
			}](),
			wantDoc:  `{"n":0}`,
			required: []string{"n"},
		},
		"struct writing a member": {
			typ: reflect.TypeFor[struct {
				S struct{ A int } `json:"s,omitempty"` //nolint:modernize // Whether v2 omits a struct under omitempty is what the row pins.
			}](),
			wantDoc:  `{"s":{"A":0}}`,
			required: []string{"s"},
		},
		"struct omitting every member": {
			typ: reflect.TypeFor[struct {
				S struct {
					A int `json:"a,omitzero"`
				} `json:"s,omitempty"` //nolint:modernize // Whether v2 omits a struct under omitempty is what the row pins.
			}](),
			wantDoc: `{}`,
		},
		"text marshaler always non-empty": {
			typ: reflect.TypeFor[struct {
				L textAlways `json:"l,omitempty"`
			}](),
			wantDoc: `{"l":"always"}`,
		},
		"json marshaler writing empty": {
			typ: reflect.TypeFor[struct {
				E jsonEmpty `json:"e,omitempty"`
			}](),
			wantDoc: `{}`,
		},
		"byte array": {
			typ: reflect.TypeFor[struct {
				A [2]byte `json:"a,omitempty"`
			}](),
			wantDoc:  `{"a":"AAA="}`,
			required: []string{"a"},
		},
		"zero-length array": {
			typ: reflect.TypeFor[struct {
				A [0]int `json:"a,omitempty"`
			}](),
			wantDoc: `{}`,
		},
		"int": {
			typ: reflect.TypeFor[struct {
				I int `json:"i,omitempty"`
			}](),
			wantDoc:  `{"i":0}`,
			required: []string{"i"},
		},
		"quoted int": {
			typ: reflect.TypeFor[struct {
				I int `json:"i,omitempty,string"`
			}](),
			wantDoc:  `{"i":"0"}`,
			required: []string{"i"},
		},
		"pointer": {
			typ: reflect.TypeFor[struct {
				P *int `json:"p,omitempty"`
			}](),
			wantDoc: `{}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc, err := jsonv2.Marshal(reflect.Zero(tc.typ).Interface())
			require.NoError(t, err)
			require.JSONEq(t, tc.wantDoc, string(doc), "the oracle marshal changed")

			s, err := jsonschema.Generate(t.Context(), tc.typ)
			require.NoError(t, err)

			assert.Equal(t, tc.required, s.Required)
			require.NoError(t, validateJSON(t.Context(), s, doc),
				"generated schema rejected the zero value's serialization: %s", doc)
		})
	}
}
