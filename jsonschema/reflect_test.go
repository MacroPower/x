package jsonschema_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

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
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"properties":{"name":{"type":"string"}},
					"required":["name"],
					"additionalProperties":{"type":"integer"}
				}
			`),
		},
		"map fallback under Draft7": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[fbMap](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
			},
			value: fbMap{Name: "n", Extra: map[string]int{"a": 1}},
			want: stringtest.Input(`
				{
					"$schema":"http://json-schema.org/draft-07/schema#",
					"type":"object",
					"properties":{"name":{"type":"string"}},
					"required":["name"],
					"additionalProperties":{"type":"integer"}
				}
			`),
		},
		"jsontext.Value fallback leaves the object open": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbValue](t.Context()) },
			value:    fbValue{Name: "n", Extra: jsontext.Value(`{"anything":[1,"two"]}`)},
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"properties":{"name":{"type":"string"}},
					"required":["name"]
				}
			`),
		},
		"pointer fallback unwraps one level": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbPtrMap](t.Context()) },
			value:    fbPtrMap{Name: "n", Extra: &map[string]int{"a": 1}},
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"properties":{"name":{"type":"string"}},
					"required":["name"],
					"additionalProperties":{"type":"integer"}
				}
			`),
		},
		"named string-kind key": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbNamedKey](t.Context()) },
			value:    fbNamedKey{Extra: map[fbKey]bool{"k": true}},
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"additionalProperties":{"type":"boolean"}
				}
			`),
		},
		"any value type renders an unrestricted constraint": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbAnyMap](t.Context()) },
			value:    fbAnyMap{Extra: map[string]any{"a": 1, "b": "two"}},
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"additionalProperties":true
				}
			`),
		},
		"self-referential value type": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbSelf](t.Context()) },
			value:    fbSelf{Name: "n", Extra: map[string]fbSelf{"kid": {Name: "k"}}},
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"$ref":"#/$defs/fbSelf",
					"$defs":{"fbSelf":{
						"type":"object",
						"properties":{"name":{"type":"string"}},
						"required":["name"],
						"additionalProperties":{"$ref":"#/$defs/fbSelf"}
					}}
				}
			`),
		},
		"open objects emit no value schema": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[fbMap](t.Context(), jsonschema.WithAdditionalProperties(true))
			},
			value: fbMap{Name: "n", Extra: map[string]int{"a": 1}},
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"properties":{"name":{"type":"string"}},
					"required":["name"]
				}
			`),
		},
		"promoted fallback": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbPromoted](t.Context()) },
			value:    fbPromoted{fbCarrier: fbCarrier{Extra: map[string]int{"a": 1}, Q: 2}, R: 3},
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"properties":{"q":{"type":"integer"},"r":{"type":"integer"}},
					"required":["q","r"],
					"additionalProperties":{"type":"integer"}
				}
			`),
		},
		"depth-0 fallback beats a promoted one": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbShallow](t.Context()) },
			value: fbShallow{
				fbCarrier: fbCarrier{Q: 2},
				Own:       map[string]string{"o": "x"},
			},
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"properties":{"q":{"type":"integer"}},
					"required":["q"],
					"additionalProperties":{"type":"string"}
				}
			`),
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
			want: stringtest.Input(`
				{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"properties":{"q":{"type":"integer"},"s":{"type":"integer"},"r":{"type":"integer"}},
					"required":["q","s","r"],
					"additionalProperties":false
				}
			`),
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

	assert.JSONEq(t, stringtest.Input(`
		{
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
		}
	`), marshalSchema(t, s))

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

		assert.JSONEq(t, stringtest.Input(`
			{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"allOf":[{"$ref":"#/$defs/fbComposeTarget"}],
				"properties":{"name":{"type":"string"},"inner":true},
				"required":["name"],
				"unevaluatedProperties":{"type":"integer"},
				"$defs":{"fbComposeTarget":{"type":"object"}}
			}
		`), marshalSchema(t, s))

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

		assert.JSONEq(t, stringtest.Input(`
			{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"allOf":[{"$ref":"#/$defs/fbGhostCarrier"}],
				"properties":{"name":{"type":"string"},"inner":true,"q":true},
				"required":["name"],
				"unevaluatedProperties":{"type":"integer"},
				"$defs":{"fbGhostCarrier":{"type":"object"}}
			}
		`), marshalSchema(t, s))

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
		generate    func() (*jsonschema.Schema, error)
		marshal     func() ([]byte, error)
		err         error
		errContains string
	}{
		"two fallbacks in one declaration": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbTwo](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbTwo{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "embedded Go struct fields A and B cannot both be a Go map or jsontext.Value",
		},
		"embed combined with another option": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbEmbedOpt](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbEmbedOpt{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "cannot have any options other than `embed` specified",
		},
		"embed combined with a name": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbEmbedNamed](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbEmbedNamed{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "cannot have any options other than `embed` specified",
		},
		"embed on an unexported field": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbUnexported](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbUnexported{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "unexported Go struct field extra cannot have non-ignored `json:\",embed\"` tag",
		},
		"marshaler-bearing map type": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbMarshalerBearing](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbMarshalerBearing{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "must not implement marshal or unmarshal methods",
		},
		"key carrying marshal methods": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbBadKey](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbBadKey{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "must have a string key that does not implement marshal or unmarshal methods",
		},
		"named type with jsontext.Value underlying": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbNamedRawEmbed](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbNamedRawEmbed{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"non-string map key": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbIntKey](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbIntKey{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"map behind two pointer levels": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbPtrPtrMap](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbPtrPtrMap{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "field Extra of type *map[string]int must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"scalar under embed": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbScalar](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbScalar{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "must be a Go struct, Go map of string key, or jsontext.Value",
		},
		"anonymous fallback form": {
			generate:    func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbAnon](t.Context()) },
			marshal:     func() ([]byte, error) { return json.Marshal(fbAnon{}) },
			err:         jsonschema.ErrInvalidJSONField,
			errContains: "embedded Go struct field fbBag of non-struct type must be explicitly given a JSON name",
		},
		"duration value type": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[fbDurationValues](t.Context()) },
			marshal: func() ([]byte, error) {
				return json.Marshal(fbDurationValues{Extra: map[string]time.Duration{"d": time.Second}})
			},
			err:         jsonschema.ErrUnsupportedType,
			errContains: "embedded fallback field Extra",
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
			err:         jsonschema.ErrUnsupportedType,
			errContains: "embedded fallback field Extra",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.ErrorIs(t, err, tc.err)
			require.ErrorContains(t, err, tc.errContains)

			_, merr := tc.marshal()
			require.Error(t, merr, "encoding/json/v2 refuses the same declaration")
		})
	}
}

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

// The tests below exercise encoding/json name resolution across a composed
// embed's boundary. Composed embeds join the field walk as ghost subtrees, so
// their promoted names must compete exactly as encoding/json's flat walk
// resolves them.

// TestGhostEmbedInternalAnnihilation pins that a JSON name annihilated inside
// a composed embed still competes in the outer resolution. The encoding/json
// walk sees X at depth 2 three times (F.A.X, F.B.X, P.Q.X) and annihilates
// them all, so the marshaled object carries no X; a replay of the embed's
// resolved winners would miss the two sightings inside F, let P.Q.X win, and
// emit a required X property the marshaled object never has.
func TestGhostEmbedInternalAnnihilation(t *testing.T) {
	t.Parallel()

	type ghostFA struct { //nolint:unused // Embedded only; exercised via reflection.
		X int `json:"X"`
	}

	type ghostFB struct { //nolint:unused // Embedded only; exercised via reflection.
		X int `json:"X"` //nolint:govet // The duplicate json name is the annihilation under test.
	}

	type ghostF struct {
		ghostFA //nolint:unused // Embedded only; exercised via reflection.
		ghostFB //nolint:unused,govet // The duplicate json name is the annihilation under test.

		Y int `json:"Y"`
	}

	type ghostQ struct { //nolint:unused // Embedded only; exercised via reflection.
		X int `json:"X"` //nolint:govet // The duplicate json name is the annihilation under test.
	}

	type ghostP struct { //nolint:unused // Embedded only; exercised via reflection.
		ghostQ //nolint:unused // Embedded only; exercised via reflection.
	}

	type ghostTop struct {
		ghostF //nolint:unused // Embedded only; exercised via reflection.
		ghostP //nolint:unused // Embedded only; exercised via reflection.

		Z int `json:"Z"`
	}

	s, err := jsonschema.GenerateFor[ghostTop](t.Context(),
		jsonschema.WithTypeSchemaFor[ghostF](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{Type: "object"},
		}),
	)
	require.NoError(t, err)

	raw, err := json.Marshal(ghostTop{
		ghostF: ghostF{Y: 3},
		Z:      5,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"Y": 3, "Z": 5}`, string(raw),
		"encoding/json annihilates X at depth 2")

	require.NotContains(t, s.Properties, "X",
		"an annihilated name never appears in the marshaled object")
	require.NotContains(t, s.Required, "X")

	c, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	assert.NoError(t, c.ValidateJSON(t.Context(), raw),
		"the generated schema must accept the type's own marshaled JSON")
}

// TestGhostDupDoesNotPropagateIntoNestedEmbed pins that a struct embedded
// twice at one depth annihilates only its direct fields: encoding/json queues
// a nested embed once, so the nested embed's promoted fields survive. W is
// promoted once through T2 even though A2 appears twice, so the composed T2
// branch must stay unconditional -- a self-annihilated ghost would wrongly
// mark it shadowed, accepting the near-miss object that omits W.
func TestGhostDupDoesNotPropagateIntoNestedEmbed(t *testing.T) {
	t.Parallel()

	type ghostT2 struct {
		W int `json:"W"`
	}

	type ghostA2 struct {
		ghostT2 //nolint:unused // Embedded only; exercised via reflection.
	}

	type ghostP1 struct {
		ghostA2 //nolint:unused // Embedded only; exercised via reflection.
	}

	type ghostP2 struct { //nolint:unused // Embedded only; exercised via reflection.
		ghostA2 //nolint:unused // Embedded only; exercised via reflection.
	}

	type ghostTop2 struct {
		ghostP1 //nolint:unused // Embedded only; exercised via reflection.
		ghostP2 //nolint:unused // Embedded only; exercised via reflection.

		Z int `json:"Z"`
	}

	s, err := jsonschema.GenerateFor[ghostTop2](t.Context(),
		jsonschema.WithTypeSchemaFor[ghostT2](jsonschema.TypeSchema{
			Value: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"W": {Type: "integer"}},
				Required:   []string{"W"},
			},
		}),
	)
	require.NoError(t, err)

	raw, err := json.Marshal(ghostTop2{
		ghostP1: ghostP1{ghostA2{ghostT2{W: 7}}},
		Z:       5,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"W": 7, "Z": 5}`, string(raw),
		"encoding/json promotes W once; the dup does not reach the nested embed")

	c, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	assert.NoError(t, c.ValidateJSON(t.Context(), raw),
		"the generated schema must accept the type's own marshaled JSON")

	assert.Error(t, c.ValidateJSON(t.Context(), []byte(`{"Z": 5}`)),
		"W always appears in the marshaled object, so the branch requiring it must be unconditional")
}

// TestGhostWonNameEvaluatedUnderClose pins that a ghost-won name stays
// evaluated when the object closes with unevaluatedProperties: false. The
// documented unrestricted zero TypeSchema renders the embed's branch as true,
// which evaluates no properties, so without a parent-side true property the
// close would reject the struct's own marshaled JSON.
func TestGhostWonNameEvaluatedUnderClose(t *testing.T) {
	t.Parallel()

	type ghostMeta struct {
		Kind string `json:"Kind"`
	}

	type ghostDoc struct {
		ghostMeta //nolint:unused // Embedded only; exercised via reflection.

		Z int `json:"Z"`
	}

	s, err := jsonschema.GenerateFor[ghostDoc](t.Context(),
		jsonschema.WithTypeSchemaFor[ghostMeta](jsonschema.TypeSchema{}),
	)
	require.NoError(t, err)

	raw, err := json.Marshal(ghostDoc{ghostMeta: ghostMeta{Kind: "k"}, Z: 5})
	require.NoError(t, err)

	c, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	assert.NoError(t, c.ValidateJSON(t.Context(), raw),
		"the branch evaluates nothing, so the parent must evaluate Kind itself")

	assert.Error(t, c.ValidateJSON(t.Context(), []byte(`{"Kind": "k", "Z": 5, "Other": 1}`)),
		"the object stays closed to names the type never marshals")
}

// A json tag name is the run of runes before the first comma, backslash, or
// quote character, whatever those runes are. A name cut short by a reserved
// rune other than the comma keeps its leading identifier instead, and is
// discarded in favor of the Go field name when it has none.

func TestGenerateFor_TagNameGrammar(t *testing.T) {
	t.Parallel()

	// An unreserved-rune name is kept as written and carries no error.
	type clean struct {
		A string `json:"a😀b"`
	}

	data, err := json.Marshal(clean{A: "1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"a😀b":"1"}`, string(data))

	s, err := jsonschema.GenerateFor[clean](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"a😀b":{"type":"string"}
		},
		"required":["a😀b"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)

	// A name cut short by a reserved rune is a malformed tag under
	// encoding/json/v2, so both marshaling and generation refuse it.
	type cut struct {
		C string `json:"x\"y"` //nolint:staticcheck // Intentional: reserved rune cuts the name.
	}

	_, err = json.Marshal(cut{C: "3"})
	require.ErrorContains(t, err, "malformed `json` tag")

	_, err = jsonschema.GenerateFor[cut](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "malformed `json` tag")
}

type taggedEmbedInner struct {
	X string `json:"x"`
}

type taggedEmbedOuter struct {
	taggedEmbedInner `json:"a😀b"` //nolint:unused // Exercised via reflection.

	Y string `json:"y"`
}

func TestGenerateFor_TagNameOnEmbeddedStructNamesIt(t *testing.T) {
	t.Parallel()

	// The name is honored, so the embed is a regular named field rather than a
	// promotion source.
	data, err := json.Marshal(taggedEmbedOuter{taggedEmbedInner: taggedEmbedInner{X: "1"}, Y: "2"})
	require.NoError(t, err)
	require.JSONEq(t, `{"a😀b":{"x":"1"},"y":"2"}`, string(data))

	s, err := jsonschema.GenerateFor[taggedEmbedOuter](t.Context())
	require.NoError(t, err)

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)
}

type namelessTagInner struct {
	X string `json:"x"`
}

type namelessTagOuter struct {
	namelessTagInner `json:"\"q"` //nolint:unused,staticcheck // Exercised via reflection; fields promoted.

	Y string `json:"y"`
}

func TestGenerateFor_NamelessTagOnEmbeddedStructPromotes(t *testing.T) {
	t.Parallel()

	// The tag's name chunk opens with a reserved rune and yields no name, so
	// the embed stays anonymous and its fields are promoted -- and the
	// malformed tag is an error under encoding/json/v2, which generation
	// mirrors.
	_, err := json.Marshal(namelessTagOuter{namelessTagInner: namelessTagInner{X: "1"}, Y: "2"})
	require.ErrorContains(t, err, "malformed `json` tag")

	_, err = jsonschema.GenerateFor[namelessTagOuter](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "malformed `json` tag")
}

// TestGenerateFor_DuplicateNameInOneDeclaration pins the same-declaration
// conflict: two fields of one struct claiming a JSON name is a refusal under
// encoding/json/v2 (v1 silently dropped both), and generation mirrors it
// with v2's verbatim text.
func TestGenerateFor_DuplicateNameInOneDeclaration(t *testing.T) {
	t.Parallel()

	type dup struct {
		A int `json:"dup"`
		B int `json:"dup"` //nolint:govet // The duplicate json name is the refusal under test.
	}

	_, err := json.Marshal(dup{})
	require.ErrorContains(t, err, `Go struct fields A and B conflict over JSON object name "dup"`)

	_, err = jsonschema.GenerateFor[dup](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, `Go struct fields A and B conflict over JSON object name "dup"`)
}

// unexportedZeroer carries the IsZero method encoding/json/v2 would have to
// call through an unexported named field under omitzero.
type unexportedZeroer struct{ A int }

func (unexportedZeroer) IsZero() bool { return false }

// TestGenerateFor_UnexportedAnonymousNamedField pins encoding/json/v2's
// export rule on the named-field path: an unexported anonymous field with a
// tag name is walked only as a struct v2 can read without calling a method.
// A non-struct type is refused outright, a struct whose marshal or omitzero
// IsZero method would be called through the unexported field is refused for
// method calls, and a plain struct is accepted as an ordinary named field.
func TestGenerateFor_UnexportedAnonymousNamedField(t *testing.T) {
	t.Parallel()

	type myInt int //nolint:unused // Embedded only; exercised via reflection.

	type nonStruct struct {
		myInt `json:"n"` //nolint:govet,unused // The tag on the unexported field is the refusal under test; exercised via reflection.
		B     int        `json:"b"`
	}

	type withMethodCall struct {
		unexportedZeroer `json:"in,omitzero"` //nolint:govet,unused // The tag on the unexported field is the refusal under test; exercised via reflection.
	}

	type plain struct{ A int } //nolint:unused // Embedded only; exercised via reflection.

	type accepted struct {
		plain `json:"in"` //nolint:govet,unused // The tag on the unexported field is the case under test; exercised via reflection.
	}

	t.Run("non-struct type is refused", func(t *testing.T) {
		t.Parallel()

		_, err := json.Marshal(nonStruct{})
		require.ErrorContains(t, err, "Go struct field myInt is not exported")

		_, err = jsonschema.GenerateFor[nonStruct](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
		require.ErrorContains(t, err, "Go struct field myInt is not exported")
	})

	t.Run("struct needing a method call is refused", func(t *testing.T) {
		t.Parallel()

		_, err := json.Marshal(withMethodCall{})
		require.ErrorContains(t, err, "Go struct field unexportedZeroer is not exported for method calls")

		_, err = jsonschema.GenerateFor[withMethodCall](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
		require.ErrorContains(t, err, "Go struct field unexportedZeroer is not exported for method calls")
	})

	t.Run("plain struct is a named field", func(t *testing.T) {
		t.Parallel()

		data, err := json.Marshal(accepted{})
		require.NoError(t, err)
		assert.JSONEq(t, `{"in":{"A":0}}`, string(data))

		s, err := jsonschema.GenerateFor[accepted](t.Context())
		require.NoError(t, err)
		assert.Contains(t, s.Properties, "in")
	})
}

type (
	namedNames []string
	namedTable map[string]int
	namedBytes []byte

	// The forbidNames type records NullForbidden through a method-based
	// extender, the registration path shouldExtract routes into $defs.
	forbidNames []string
	// The forbidCyc type holds itself, so its body always lands in $defs
	// through the cycle guard whatever WithDefinitions says.
	forbidCyc []forbidCyc
)

func (forbidNames) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Nullability = jsonschema.NullForbidden

	return nil
}

func (forbidCyc) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Nullability = jsonschema.NullForbidden

	return nil
}

// TestGenerateFor_PointerToNamedContainerAdmitsNull pins the pointer
// occurrence null on a named slice, map, and byte slice. A named container
// is built bare for the cycle guard, and the withheld pointer occurrence has
// to be restored on the inline node, or the schema rejects the null the
// marshal writes for a nil pointer.
func TestGenerateFor_PointerToNamedContainerAdmitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		PN *namedNames   `json:"pn"`
		PT *namedTable   `json:"pt"`
		PB *namedBytes   `json:"pb"`
		E  []*namedNames `json:"e"`
		N  namedNames    `json:"n"`
	}

	tests := map[string]struct {
		gen     []jsonschema.GenerateOption
		marshal []json.Options
	}{
		"defaults":        {},
		"definitions off": {gen: []jsonschema.GenerateOption{jsonschema.WithDefinitions(false)}},
		"nil slice as null": {
			gen:     []jsonschema.GenerateOption{jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true))},
			marshal: []json.Options{json.FormatNilSliceAsNull(true)},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[doc](t.Context(), tc.gen...)
			require.NoError(t, err)

			for _, key := range []string{"pn", "pt", "pb"} {
				assert.Equal(t, []string{"null", propertyBase(t, s, key)}, s.Properties[key].Types,
					"property %q must admit the nil pointer's null", key)
			}

			assert.Equal(t, []string{"null", "array"}, s.Properties["e"].Items.Types,
				"a pointer element must admit null")

			data, err := json.Marshal(doc{E: []*namedNames{nil}}, tc.marshal...)
			require.NoError(t, err)
			require.NoError(t, validateJSON(t.Context(), s, data),
				"generated schema rejected the struct's own serialization: %s", data)
		})
	}
}

// propertyBase names the non-null type a nullable property schema carries.
func propertyBase(t *testing.T, s *jsonschema.Schema, key string) string {
	t.Helper()

	types := s.Properties[key].Types
	require.Len(t, types, 2, "property %q types: %v", key, types)

	for _, typ := range types {
		if typ != "null" {
			return typ
		}
	}

	t.Fatalf("property %q has no non-null type: %v", key, types)

	return ""
}

// TestGenerateFor_NullForbiddenNamedSliceUnderFormatNull pins that a
// NullForbidden stance clears the null FormatNilSliceAsNull folds into a
// named slice's body before that body is shared through $defs. The slice
// builder adds the format null on the bare build, and a reference carries
// the stance only in its own null decision, so without the veto an
// extracted or cyclic body admits the null the inline node refuses.
func TestGenerateFor_NullForbiddenNamedSliceUnderFormatNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		S forbidNames `json:"s"`
	}

	type cyc struct {
		C forbidCyc `json:"c"`
	}

	tests := map[string]struct {
		defs     bool
		cyclic   bool
		defName  string
		instance string
		null     string
	}{
		"definitions on": {
			defs: true, defName: "forbidNames", instance: `{"s":[]}`, null: `{"s":null}`,
		},
		"definitions off": {
			instance: `{"s":[]}`, null: `{"s":null}`,
		},
		"cyclic under definitions on": {
			defs: true, cyclic: true, defName: "forbidCyc", instance: `{"c":[[]]}`, null: `{"c":null}`,
		},
		"cyclic under definitions off": {
			cyclic: true, defName: "forbidCyc", instance: `{"c":[[]]}`, null: `{"c":null}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := []jsonschema.GenerateOption{
				jsonschema.WithDefinitions(tc.defs),
				jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true)),
			}

			var (
				s   *jsonschema.Schema
				err error
			)

			if tc.cyclic {
				s, err = jsonschema.GenerateFor[cyc](t.Context(), opts...)
			} else {
				s, err = jsonschema.GenerateFor[doc](t.Context(), opts...)
			}

			require.NoError(t, err)

			body := s.Properties["s"]
			if tc.defName != "" {
				require.Contains(t, s.Defs, tc.defName)

				body = s.Defs[tc.defName]
			}

			assert.Equal(t, "array", body.Type, "the shared body must carry no format null")
			assert.Empty(t, body.Types, "the shared body must carry no format null")

			require.NoError(t, validateJSON(t.Context(), s, []byte(tc.instance)))
			require.Error(t, validateJSON(t.Context(), s, []byte(tc.null)),
				"NullForbidden must reject the null the format option would admit")
		})
	}
}

// numberOmitEmpty holds the one string-kind field omitempty never omits:
// jsonv1.Number's MarshalJSONTo writes 0 for the empty value, so encoding/json/v2
// always encodes it and the field stays required. A pointer to it is omitted
// when nil, as any pointer is, and omitzero omits the zero Number.
type numberOmitEmpty struct {
	N  jsonv1.Number  `json:"n,omitempty"`
	NP *jsonv1.Number `json:"np,omitempty"`
	NZ jsonv1.Number  `json:"nz,omitzero"`
}

func TestGenerateFor_JSONNumberOmitEmptyStaysRequired(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[numberOmitEmpty](t.Context())
	require.NoError(t, err)

	assert.Equal(t, []string{"n"}, s.Required)

	// Oracle: v2 keeps n even for the zero value, so the marshaled zero value
	// must validate and a document without n must not.
	out, err := json.Marshal(numberOmitEmpty{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":0}`, string(out))
	require.NoError(t, validateJSON(t.Context(), s, out))
	require.Error(t, validateJSON(t.Context(), s, []byte(`{}`)))
}

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
				N jsonv1.Number `json:"n,omitempty"`
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

			doc, err := json.Marshal(reflect.Zero(tc.typ).Interface())
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
