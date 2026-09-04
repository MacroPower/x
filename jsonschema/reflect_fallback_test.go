package jsonschema_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
)

// The types here pin the embedded-fallback model. A non-anonymous
// json:",embed" field of a string-keyed map or jsontext.Value splices its
// members into the parent JSON object, and the generated schema carries the
// map's value schema as the object's extra-member constraint.

type fbMap struct {
	Name  string         `json:"name"`
	Extra map[string]int `json:",embed"`
}

type fbValue struct {
	Name  string         `json:"name"`
	Extra jsontext.Value `json:",embed"`
}

type fbPtrMap struct {
	Name  string          `json:"name"`
	Extra *map[string]int `json:",embed"`
}

// fbKey is a named string-kind key, which qualifies like the builtin string.
type fbKey string

type fbNamedKey struct {
	Extra map[fbKey]bool `json:",embed"`
}

type fbAnyMap struct {
	Extra map[string]any `json:",embed"`
}

// fbSelf carries itself as the fallback value type, so the value schema is a
// self-reference into $defs.
type fbSelf struct {
	Name  string            `json:"name"`
	Extra map[string]fbSelf `json:",embed"`
}

// fbCarrier is an embeddable struct carrying a fallback, promoted at depth 1.
type fbCarrier struct {
	Extra map[string]int `json:",embed"`
	Q     int            `json:"q"`
}

// fbCarrierB is a second carrier for the same-depth tie.
type fbCarrierB struct {
	More map[string]bool `json:",embed"`
	S    int             `json:"s"`
}

type fbPromoted struct {
	fbCarrier //nolint:unused // Promoted via reflection.

	R int `json:"r"`
}

// fbShallow declares its own fallback beside a promoted one, so depth 0 wins.
type fbShallow struct {
	fbCarrier //nolint:unused // Promoted via reflection.

	Own map[string]string `json:",embed"`
}

// fbTie promotes two fallbacks at depth 1, which silently drops both; the
// object stays closed and v2 splices nothing.
type fbTie struct {
	fbCarrier  //nolint:unused // Promoted via reflection.
	fbCarrierB //nolint:unused // Promoted via reflection.

	R int `json:"r"`
}

func TestGenerateFor_EmbeddedFallbackSchemas(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		value    any
		want     string
	}{
		"map fallback": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbMap](t.Context()) },
			value:    fbMap{Name: "n", Extra: map[string]int{"a": 1}},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"required":["name"],
				"additionalProperties":{"type":"integer"}
			}`,
		},
		"map fallback under Draft7": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[fbMap](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
			},
			value: fbMap{Name: "n", Extra: map[string]int{"a": 1}},
			want: `{
				"$schema":"http://json-schema.org/draft-07/schema#",
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"required":["name"],
				"additionalProperties":{"type":"integer"}
			}`,
		},
		"jsontext.Value fallback leaves the object open": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbValue](t.Context()) },
			value:    fbValue{Name: "n", Extra: jsontext.Value(`{"anything":[1,"two"]}`)},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"required":["name"]
			}`,
		},
		"pointer fallback unwraps one level": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbPtrMap](t.Context()) },
			value:    fbPtrMap{Name: "n", Extra: &map[string]int{"a": 1}},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"required":["name"],
				"additionalProperties":{"type":"integer"}
			}`,
		},
		"named string-kind key": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbNamedKey](t.Context()) },
			value:    fbNamedKey{Extra: map[fbKey]bool{"k": true}},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"additionalProperties":{"type":"boolean"}
			}`,
		},
		"any value type renders an unrestricted constraint": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbAnyMap](t.Context()) },
			value:    fbAnyMap{Extra: map[string]any{"a": 1, "b": "two"}},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"additionalProperties":true
			}`,
		},
		"self-referential value type": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbSelf](t.Context()) },
			value:    fbSelf{Name: "n", Extra: map[string]fbSelf{"kid": {Name: "k"}}},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"$ref":"#/$defs/fbSelf",
				"$defs":{"fbSelf":{
					"type":"object",
					"properties":{"name":{"type":"string"}},
					"required":["name"],
					"additionalProperties":{"$ref":"#/$defs/fbSelf"}
				}}
			}`,
		},
		"open objects emit no value schema": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[fbMap](t.Context(), jsonschema.WithAdditionalProperties(true))
			},
			value: fbMap{Name: "n", Extra: map[string]int{"a": 1}},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"required":["name"]
			}`,
		},
		"promoted fallback": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbPromoted](t.Context()) },
			value:    fbPromoted{fbCarrier: fbCarrier{Extra: map[string]int{"a": 1}, Q: 2}, R: 3},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"q":{"type":"integer"},"r":{"type":"integer"}},
				"required":["q","r"],
				"additionalProperties":{"type":"integer"}
			}`,
		},
		"depth-0 fallback beats a promoted one": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbShallow](t.Context()) },
			value: fbShallow{
				fbCarrier: fbCarrier{Q: 2},
				Own:       map[string]string{"o": "x"},
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"q":{"type":"integer"}},
				"required":["q"],
				"additionalProperties":{"type":"string"}
			}`,
		},
		"same-depth tie drops both and stays closed": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbTie](t.Context()) },
			// V2 splices nothing for the dropped fallbacks, so the closed
			// object accepts the marshaled output even with both maps set.
			value: fbTie{
				fbCarrier:  fbCarrier{Extra: map[string]int{"a": 1}, Q: 2},
				fbCarrierB: fbCarrierB{More: map[string]bool{"m": true}, S: 3},
				R:          4,
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"q":{"type":"integer"},"s":{"type":"integer"},"r":{"type":"integer"}},
				"required":["q","s","r"],
				"additionalProperties":false
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			assert.JSONEq(t, tc.want, marshalSchema(t, s))

			data, err := json.Marshal(tc.value)
			require.NoError(t, err)

			require.NoError(t, validateJSON(t.Context(), s, data),
				"generated schema rejected the value the type serializes to: %s", data)
		})
	}
}

// fbOverrideField carries a fallback inside an inline struct field whose
// jsonschema tag overrides the type wholesale. The override's rebuild must
// keep the fallback value node, or the value schema's null branch and $ref
// resolution silently drop from the extra-member slot.
type fbOverrideField struct {
	F struct {
		Name  string          `json:"name"`
		Extra map[string]*int `json:",embed"`
	} `json:"f" jsonschema:"type=object"`
}

func TestGenerateFor_EmbeddedFallbackTypeOverriddenField(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[fbOverrideField](t.Context())
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"f":{
			"type":"object",
			"properties":{"name":{"type":"string"}},
			"required":["name"],
			"additionalProperties":{"anyOf":[{"type":"integer"},{"type":"null"}]}
		}},
		"required":["f"],
		"additionalProperties":false
	}`, marshalSchema(t, s))

	v := fbOverrideField{}
	v.F.Name = "n"
	v.F.Extra = map[string]*int{"a": nil}

	data, err := json.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, validateJSON(t.Context(), s, data))
}

// fbComposeTarget is the embed a WithTypeSchema override intercepts, so the
// enclosing struct composes it via allOf.
type fbComposeTarget struct {
	Inner string `json:"inner"`
}

type fbComposed struct {
	fbComposeTarget //nolint:unused // Composed via allOf.

	Name  string         `json:"name"`
	Extra map[string]int `json:",embed"`
}

type fbValueComposed struct {
	fbComposeTarget //nolint:unused // Composed via allOf.

	Name  string         `json:"name"`
	Extra jsontext.Value `json:",embed"`
}

// fbGhostCarrier embeds fbCarrier, so composing it puts the fallback inside
// the composed embed's ghost subtree rather than at depth 0.
type fbGhostCarrier struct {
	fbCarrier //nolint:unused // Promoted via reflection.

	Inner string `json:"inner"`
}

type fbGhostComposed struct {
	fbGhostCarrier //nolint:unused // Composed via allOf.

	Name string `json:"name"`
}

// fbShadowTarget promotes two names, one of which the outer struct shadows,
// so its composition is partly shadowed.
type fbShadowTarget struct {
	Inner string `json:"inner"`
	Both  string `json:"both"`
}

type fbShadowed struct {
	fbShadowTarget //nolint:unused // Composed via allOf.

	Both  int            `json:"both"`
	Extra map[string]int `json:",embed"`
}

func TestGenerateFor_EmbeddedFallbackComposed(t *testing.T) {
	t.Parallel()

	composeTarget := jsonschema.WithTypeSchema(
		reflect.TypeFor[fbComposeTarget](),
		jsonschema.TypeSchema{Value: &jsonschema.Schema{Type: "object"}},
	)
	shadowTarget := jsonschema.WithTypeSchema(
		reflect.TypeFor[fbShadowTarget](),
		jsonschema.TypeSchema{Value: &jsonschema.Schema{Type: "object"}},
	)

	t.Run("map fallback beside a composed embed under 2020-12", func(t *testing.T) {
		t.Parallel()

		// The allOf branches evaluate the promoted names, so the fallback's
		// value schema moves to unevaluatedProperties and judges exactly the
		// spliced members; the ghost-won "inner" is punched as a true
		// property so the branch's non-evaluation cannot reject it.
		s, err := jsonschema.GenerateFor[fbComposed](t.Context(), composeTarget)
		require.NoError(t, err)

		assert.JSONEq(t, `{
			"$schema":"https://json-schema.org/draft/2020-12/schema",
			"type":"object",
			"allOf":[{"$ref":"#/$defs/fbComposeTarget"}],
			"properties":{"name":{"type":"string"},"inner":true},
			"required":["name"],
			"unevaluatedProperties":{"type":"integer"},
			"$defs":{"fbComposeTarget":{"type":"object"}}
		}`, marshalSchema(t, s))

		data, err := json.Marshal(fbComposed{
			fbComposeTarget: fbComposeTarget{Inner: "i"},
			Name:            "n",
			Extra:           map[string]int{"z": 3},
		})
		require.NoError(t, err)
		require.NoError(t, validateJSON(t.Context(), s, data))

		// The value schema keeps its teeth through unevaluatedProperties: a
		// spliced member of the wrong type is rejected.
		require.Error(t, validateJSON(t.Context(), s,
			[]byte(`{"name":"n","inner":"i","z":"not an integer"}`)))
	})

	t.Run("map fallback beside a composed embed under Draft7", func(t *testing.T) {
		t.Parallel()

		// Draft-07 has no unevaluatedProperties, and additionalProperties
		// would wrongly constrain the embed-promoted names, so the object
		// stays open and the value schema is dropped.
		s, err := jsonschema.GenerateFor[fbComposed](t.Context(), composeTarget,
			jsonschema.WithDraft(jsonschema.Draft7))
		require.NoError(t, err)

		assert.Nil(t, s.AdditionalProperties)
		assert.Nil(t, s.UnevaluatedProperties)

		data, err := json.Marshal(fbComposed{
			fbComposeTarget: fbComposeTarget{Inner: "i"},
			Name:            "n",
			Extra:           map[string]int{"z": 3},
		})
		require.NoError(t, err)
		require.NoError(t, validateJSON(t.Context(), s, data))
	})

	t.Run("jsontext.Value fallback beside a composed embed", func(t *testing.T) {
		t.Parallel()

		// The spliced members are arbitrary JSON, so the object is fully
		// open under both dialects and the ghost punching is skipped: with
		// no unevaluatedProperties there is nothing a punched property would
		// guard.
		for _, draft := range []jsonschema.Draft{jsonschema.Draft2020, jsonschema.Draft7} {
			s, err := jsonschema.GenerateFor[fbValueComposed](t.Context(), composeTarget,
				jsonschema.WithDraft(draft))
			require.NoError(t, err)

			assert.Nil(t, s.AdditionalProperties)
			assert.Nil(t, s.UnevaluatedProperties)
			assert.NotContains(t, s.Properties, "inner",
				"an open object needs no punched ghost property")

			data, err := json.Marshal(fbValueComposed{
				fbComposeTarget: fbComposeTarget{Inner: "i"},
				Name:            "n",
				Extra:           jsontext.Value(`{"z":[1,"two"]}`),
			})
			require.NoError(t, err)
			require.NoError(t, validateJSON(t.Context(), s, data))
		}
	})

	t.Run("fallback inside a composed embed's ghost subtree", func(t *testing.T) {
		t.Parallel()

		// The ghost walk of the composed fbGhostCarrier sights the fallback,
		// which competes in dominance normally. The generator models the
		// winner on the parent exactly like a depth-0 fallback, since v2
		// splices its members into the parent object regardless of
		// composition.
		ghostTarget := jsonschema.WithTypeSchema(
			reflect.TypeFor[fbGhostCarrier](),
			jsonschema.TypeSchema{Value: &jsonschema.Schema{Type: "object"}},
		)

		s, err := jsonschema.GenerateFor[fbGhostComposed](t.Context(), ghostTarget)
		require.NoError(t, err)

		assert.JSONEq(t, `{
			"$schema":"https://json-schema.org/draft/2020-12/schema",
			"type":"object",
			"allOf":[{"$ref":"#/$defs/fbGhostCarrier"}],
			"properties":{"name":{"type":"string"},"inner":true,"q":true},
			"required":["name"],
			"unevaluatedProperties":{"type":"integer"},
			"$defs":{"fbGhostCarrier":{"type":"object"}}
		}`, marshalSchema(t, s))

		data, err := json.Marshal(fbGhostComposed{
			fbGhostCarrier: fbGhostCarrier{
				fbCarrier: fbCarrier{Extra: map[string]int{"bag": 5}, Q: 1},
				Inner:     "i",
			},
			Name: "n",
		})
		require.NoError(t, err)
		require.NoError(t, validateJSON(t.Context(), s, data))

		require.Error(t, validateJSON(t.Context(), s,
			[]byte(`{"name":"n","inner":"i","q":1,"bag":"not an integer"}`)))
	})

	t.Run("partly shadowed composition drops the value schema", func(t *testing.T) {
		t.Parallel()

		// The shadow-partial state leaves the object open with or without a
		// fallback, so the fallback's value schema is dropped with it.
		s, err := jsonschema.GenerateFor[fbShadowed](t.Context(), shadowTarget)
		require.NoError(t, err)

		assert.Nil(t, s.AdditionalProperties)
		assert.Nil(t, s.UnevaluatedProperties)

		data, err := json.Marshal(fbShadowed{
			fbShadowTarget: fbShadowTarget{Inner: "i", Both: "shadowed"},
			Both:           2,
			Extra:          map[string]int{"z": 3},
		})
		require.NoError(t, err)
		require.NoError(t, validateJSON(t.Context(), s, data))
	})
}

// Each refusal type below is a declaration encoding/json/v2 itself refuses
// to marshal, asserted by the marshal leg beside the generation error.

type fbTwo struct {
	A map[string]int `json:",embed"`
	B jsontext.Value `json:",embed"`
}

type fbEmbedOpt struct {
	Extra map[string]int `json:",embed,omitempty"`
}

type fbEmbedNamed struct {
	Extra map[string]int `json:"x,embed"`
}

type fbUnexported struct {
	extra map[string]int `json:",embed"` //nolint:unused,govet,staticcheck // Tag under test: a json tag on an unexported field.
}

// fbMarshalerMap carries its own MarshalJSON, which v2 refuses under ",embed".
type fbMarshalerMap map[string]int

// MarshalJSON marshals the map as an empty object.
func (fbMarshalerMap) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

type fbMarshalerBearing struct {
	Extra fbMarshalerMap `json:",embed"`
	Y     int            `json:"y"`
}

// fbBadKey's key is string-kind but carries json.Number's marshal methods.
type fbBadKey struct {
	Extra map[jsonv1.Number]int `json:",embed"`
}

// fbNamedRaw has jsontext.Value's underlying type without its identity.
type fbNamedRaw []byte

type fbNamedRawEmbed struct {
	Extra fbNamedRaw `json:",embed"`
}

type fbIntKey struct {
	Extra map[int]string `json:",embed"`
}

type fbScalar struct {
	Extra int `json:",embed"`
}

// fbPtrPtrMap sits one pointer level past the single unnamed level v2's
// indirectType unwraps, so the field classifies as *map and is refused.
type fbPtrPtrMap struct {
	Extra **map[string]int `json:",embed"`
}

// fbBag is an anonymous-embeddable map, refused however it is tagged.
type fbBag map[string]int //nolint:unused // Embedded below; read via reflection only.

type fbAnon struct {
	fbBag `json:",embed"` //nolint:unused,staticcheck // Tag under test: ",embed" on an anonymous non-struct.
}

type fbDurationValues struct {
	Extra map[string]time.Duration `json:",embed"`
}

type fbFuncValues struct {
	Extra map[string]func() `json:",embed"`
}

func TestGenerateFor_EmbeddedFallbackRefused(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		marshal  func() ([]byte, error)
		errIs    error
		wantErr  string
	}{
		"two fallbacks in one declaration": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbTwo](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbTwo{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "embedded Go struct fields A and B cannot both be a Go map or jsontext.Value",
		},
		"embed combined with another option": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbEmbedOpt](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbEmbedOpt{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "cannot have any options other than `embed` specified",
		},
		"embed combined with a name": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbEmbedNamed](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbEmbedNamed{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "cannot have any options other than `embed` specified",
		},
		"embed on an unexported field": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbUnexported](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbUnexported{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "unexported Go struct field extra cannot have non-ignored `json:\",embed\"` tag",
		},
		"marshaler-bearing map type": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbMarshalerBearing](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbMarshalerBearing{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "must not implement marshal or unmarshal methods",
		},
		"key carrying marshal methods": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbBadKey](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbBadKey{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "must have a string key that does not implement marshal or unmarshal methods",
		},
		"named type with jsontext.Value underlying": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbNamedRawEmbed](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbNamedRawEmbed{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"non-string map key": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbIntKey](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbIntKey{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"map behind two pointer levels": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbPtrPtrMap](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbPtrPtrMap{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "field Extra of type *map[string]int must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"scalar under embed": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbScalar](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbScalar{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"anonymous fallback form": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbAnon](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(fbAnon{}) },
			errIs:    jsonschema.ErrInvalidJSONField,
			wantErr:  "embedded Go struct field fbBag of non-struct type must be explicitly given a JSON name",
		},
		"duration value type": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbDurationValues](t.Context()) },
			marshal: func() ([]byte, error) {
				return json.Marshal(fbDurationValues{Extra: map[string]time.Duration{"d": time.Second}})
			},
			errIs:   jsonschema.ErrUnsupportedType,
			wantErr: "embedded fallback field Extra",
		},
		"unrepresentable value type under open objects": {
			// V is built before WithAdditionalProperties is consulted, so a
			// bad value type refuses generation under every option.
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[fbFuncValues](t.Context(), jsonschema.WithAdditionalProperties(true))
			},
			marshal: func() ([]byte, error) {
				return json.Marshal(fbFuncValues{Extra: map[string]func(){"f": func() {}}})
			},
			errIs:   jsonschema.ErrUnsupportedType,
			wantErr: "embedded fallback field Extra",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.ErrorIs(t, err, tc.errIs)
			require.ErrorContains(t, err, tc.wantErr)

			_, merr := tc.marshal()
			require.Error(t, merr, "encoding/json/v2 refuses the same declaration")
		})
	}
}
