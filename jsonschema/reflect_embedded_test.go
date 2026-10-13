package jsonschema_test

import (
	"context"
	"encoding"
	"encoding/json/v2"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// GenList is a generic named slice type. Embedded as GenList[int], its
// [reflect.StructField.Name] is the bare identifier "GenList", while
// [reflect.Type.Name] is "GenList[int]"; encoding/json keys the field by the
// former.
type GenList[T any] []T

type hasGenericEmbed struct {
	GenList[int]

	B string `json:"b"`
}

func TestGenerateFor_EmbeddedGenericNonStructType(t *testing.T) {
	t.Parallel()

	// An embedded non-struct without an explicit name is an error under
	// encoding/json/v2, generic instantiations included.
	_, err := json.Marshal(hasGenericEmbed{GenList: GenList[int]{1}, B: "x"})
	require.ErrorContains(t, err, "must be explicitly given a JSON name")

	_, err = jsonschema.GenerateFor[hasGenericEmbed](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "GenList")
}

// Encoding/json/v2 refuses an embedded interface outright: an embedded
// non-struct must be explicitly given a JSON name. Generation refuses the
// same declarations; an interface as a regular named field stays supported.

// LeafIface is an exported interface embedded as a leaf field.
type LeafIface interface{ LeafValue() string }

// leafIfaceObject is a concrete LeafIface that marshals as a JSON object
// matching the WithTypeSchema override used in the tests below.
type leafIfaceObject struct {
	Kind string `json:"kind"`
}

func (o leafIfaceObject) LeafValue() string { return o.Kind }

type hasLeafIface struct {
	LeafIface

	Name string `json:"name"`
}

func TestGenerateFor_EmbeddedInterfaceRefused(t *testing.T) {
	t.Parallel()

	// Encoding/json/v2 refuses the embedded interface, whatever the value.
	_, err := json.Marshal(hasLeafIface{Name: "x"})
	require.ErrorContains(t, err, "must be explicitly given a JSON name")

	// Generation refuses the same declaration. A concrete implementation
	// stays usable as a regular field (asserted by the field tests below).
	_, err = jsonschema.GenerateFor[hasLeafIface](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "must be explicitly given a JSON name")
}

// leafIfaceOverride is the WithTypeSchema override describing what every
// concrete LeafIface implementation marshals as in these tests.
func leafIfaceOverride() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"kind": {Type: "string"},
		},
		Required: []string{"kind"},
	}
}

func TestGenerateFor_EmbeddedInterfaceWithTypeSchemaOverride(t *testing.T) {
	t.Parallel()

	// A WithTypeSchema override cannot rescue an embedded interface: the
	// refusal is encoding/json/v2's, about the declaration rather than the
	// schema, so generation reports it before the override is consulted.
	_, err := jsonschema.GenerateFor[hasLeafIface](
		t.Context(),
		jsonschema.WithTypeSchemaFor[LeafIface](jsonschema.TypeSchema{Value: leafIfaceOverride()}),
	)
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "must be explicitly given a JSON name")
}

func TestGenerateFor_InterfaceFieldTypeSchemaAdmitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		Val LeafIface `json:"val"`
	}

	nilData, err := json.Marshal(doc{})
	require.NoError(t, err)
	require.JSONEq(t, `{"val":null}`, string(nilData))

	setData, err := json.Marshal(doc{Val: leafIfaceObject{Kind: "a"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"val":{"kind":"a"}}`, string(setData))

	s, err := jsonschema.GenerateFor[doc](
		t.Context(),
		jsonschema.WithTypeSchemaFor[LeafIface](jsonschema.TypeSchema{Value: leafIfaceOverride()}),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// An intercepted interface schema admits null on a regular field too.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"val":{
				"anyOf":[
					{
						"type":"object",
						"properties":{"kind":{"type":"string"}},
						"required":["kind"]
					},
					{"type":"null"}
				]
			}
		},
		"required":["val"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-field serialization: %s", nilData)
	require.NoError(t, validateJSON(t.Context(), s, setData),
		"generated schema rejected the non-nil-field serialization: %s", setData)
}

// TextIface is an exported interface whose method set includes
// [encoding.TextMarshaler]. A non-nil value marshals as a string via
// MarshalText; a nil interface marshals as null.
type TextIface interface {
	encoding.TextMarshaler

	TextValue() string
}

// textVal is a concrete TextMarshaler stored in TextIface fields.
type textVal string

func (v textVal) MarshalText() ([]byte, error) { return []byte(v), nil }

func (v textVal) TextValue() string { return string(v) }

func TestGenerateFor_TextMarshalerInterfaceFieldAdmitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		T TextIface `json:"t"`
	}

	nilData, err := json.Marshal(doc{})
	require.NoError(t, err)
	require.JSONEq(t, `{"t":null}`, string(nilData))

	setData, err := json.Marshal(doc{T: textVal("hi")})
	require.NoError(t, err)
	require.JSONEq(t, `{"t":"hi"}`, string(setData))

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// A non-nil value marshals via MarshalText as a string; a nil interface
	// marshals as null, so the string schema admits null alongside.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"t":{"anyOf":[{"type":"string"},{"type":"null"}]}
		},
		"required":["t"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-field serialization: %s", nilData)
	require.NoError(t, validateJSON(t.Context(), s, setData),
		"generated schema rejected the non-nil-field serialization: %s", setData)
}

// embedsTextIface embeds the exported TextMarshaler interface. Its direct
// MarshalJSON keeps the promoted-TextMarshaler interception from firing on the
// outer type, so the struct reflects its fields and reaches the embedded
// interface's refusal.
type embedsTextIface struct {
	TextIface

	Name string `json:"name"`
}

func (embedsTextIface) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

func TestGenerateFor_EmbeddedTextMarshalerInterfaceRefused(t *testing.T) {
	t.Parallel()

	// Encoding/json/v2 marshals the outer value through its direct
	// MarshalJSON and never walks the fields, but the generator's documented
	// policy reflects a direct marshaler's fields as its best guess, and the
	// reflected declaration carries an embedded interface v2 refuses to walk.
	// The conservative answer is the refusal.
	_, err := jsonschema.GenerateFor[embedsTextIface](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "must be explicitly given a JSON name")
}

// shadowedProvided is an embedded type intercepted by a WithTypeSchema
// override, so its composition rides an allOf branch instead of promoting
// fields. Its schema asserts X is a required integer.
type shadowedProvided struct {
	X int `json:"X"`
}

// shadowedProvidedWide adds a second field so only part of the embed's
// contribution can be shadowed.
type shadowedProvidedWide struct {
	X int `json:"X"`
	Y int `json:"Y"`
}

// shadowedProvidedB duplicates shadowedProvided as a distinct type, so two
// composed embeds can collide with each other on the same promoted name.
type shadowedProvidedB struct {
	X int `json:"X"`
}

// shadowedUntagged promotes X without a json tag, so a tagged real field at
// the same depth wins encoding/json's tie-break against it.
type shadowedUntagged struct {
	X int
}

// shadowedProvidedFallback carries a fallback beside its field, so a
// shallower fallback on an enclosing type wins the dominance over it.
type shadowedProvidedFallback struct {
	X     int            `json:"X"`
	Extra map[string]int `json:",embed"`
}

// shadowedProvidedNested composes shadowedProvided inside itself, so the
// name X reaches an enclosing struct through two composed levels and only
// the outer branch carries it there.
type shadowedProvidedNested struct {
	shadowedProvided //nolint:unused // Embedded only; exercised via reflection.
}

// TestGenerateShadowedComposedEmbed pins the module's core accept property
// for allOf-composed embeds under field shadowing: encoding/json resolves a
// composed embed's promoted fields normally, so a shallower outer field wins
// the JSON name and the marshaled object carries the outer value where the
// composed schema asserts the embed's constraints. The generated schema must
// still accept everything the type marshals: the shadowed branch becomes
// conditional, and a partially shadowed composition leaves the object open.
func TestGenerateShadowedComposedEmbed(t *testing.T) {
	t.Parallel()

	provided := jsonschema.TypeSchema{Value: &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"X": {Type: "integer"},
		},
		Required: []string{"X"},
	}}

	providedWide := jsonschema.TypeSchema{Value: &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"X": {Type: "integer"},
			"Y": {Type: "integer"},
		},
		Required: []string{"X"},
	}}

	t.Run("fully shadowed embed accepts marshaled output", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvided //nolint:unused // Embedded only; exercised via reflection.

			X string `json:"X"`
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{X: "hi"})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":"hi"}`, string(data))

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the generated schema must accept the type's own marshaled JSON")
	})

	t.Run("partially shadowed embed accepts marshaled output", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvidedWide //nolint:unused // Embedded only; exercised via reflection.

			X string `json:"X"`
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvidedWide](), providedWide))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{X: "hi"})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":"hi","Y":0}`, string(data))

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the unshadowed field must not be rejected as unevaluated")
	})

	t.Run("embed name shadows a deeper real field", func(t *testing.T) {
		t.Parallel()

		type deep2 struct { //nolint:unused // Embedded only; exercised via reflection.
			X string `json:"X"`
		}

		type deep1 struct { //nolint:unused // Embedded only; exercised via reflection.
			deep2 //nolint:unused // Embedded only; exercised via reflection.
		}

		type outer struct {
			shadowedProvided //nolint:unused // Embedded only; exercised via reflection.
			deep1            //nolint:unused // Embedded only; exercised via reflection.
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{shadowedProvided: shadowedProvided{X: 7}})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":7}`, string(data),
			"encoding/json promotes the embed's shallower field")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the schema must assert the embed's winning field, not the deeper loser")
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"X":"hi"}`)),
			"the deeper real field lost the name and must not type it as a string")
	})

	t.Run("same-depth tie with a real field annihilates both", func(t *testing.T) {
		t.Parallel()

		type embReal struct {
			X string `json:"X"`
		}

		type outer struct {
			shadowedProvided //nolint:unused // Embedded only; exercised via reflection.
			embReal          //nolint:unused,govet // The duplicate json name is the annihilation under test.
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{shadowedProvided: shadowedProvided{X: 7}, embReal: embReal{X: "hi"}})
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(data),
			"encoding/json annihilates the same-depth tie")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"neither the branch nor the real field may require the annihilated name")
	})

	t.Run("same-depth tie won by the tagged real field", func(t *testing.T) {
		t.Parallel()

		type embTagged struct {
			X2 string `json:"X"`
		}

		type outer struct {
			shadowedUntagged //nolint:unused // Embedded only; exercised via reflection.
			embTagged        //nolint:unused // Embedded only; exercised via reflection.
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedUntagged](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{embTagged: embTagged{X2: "hi"}})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":"hi"}`, string(data),
			"encoding/json's tag tie-break keeps the tagged real field")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the tagged winner's schema must accept its own marshaled value")
	})

	t.Run("colliding composed embeds both become conditional", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvided  //nolint:unused // Embedded only; exercised via reflection.
			shadowedProvidedB //nolint:unused,govet // The duplicate json name is the collision under test.
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvidedB](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{
			shadowedProvided:  shadowedProvided{X: 1},
			shadowedProvidedB: shadowedProvidedB{X: 2},
		})
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(data),
			"encoding/json annihilates the cross-embed collision")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"neither branch may stay unconditionally required")
	})

	// The shadow marking once ignored a composed embed's fallback, so a
	// provider schema constraining the embed's extra members stayed an
	// unconditional branch while encoding/json spliced the enclosing type's
	// own fallback into the object instead.
	t.Run("kept fallback shadows the embed's own", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvidedFallback //nolint:unused // Embedded only; exercised via reflection.

			M map[string]string `json:",embed"`
		}

		// The provider deliberately constrains the extra members the embed's
		// fallback would carry, which the kept string-valued fallback breaks.
		providedFallback := jsonschema.TypeSchema{Value: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"X": {Type: "integer"},
			},
			Required:             []string{"X"},
			AdditionalProperties: &jsonschema.Schema{Type: "integer"},
		}}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvidedFallback](), providedFallback))
		require.NoError(t, err)

		require.Len(t, s.AllOf, 1)
		require.Len(t, s.AllOf[0].AnyOf, 2, "the shadowed branch is conditional")
		assert.NotEmpty(t, s.AllOf[0].AnyOf[0].Ref)
		assert.Equal(t, &jsonschema.Schema{}, s.AllOf[0].AnyOf[1], "the escape arm is the true schema")
		assert.Nil(t, s.AdditionalProperties, "the embed still promotes X, so the parent stays open")
		assert.Nil(t, s.UnevaluatedProperties, "the embed still promotes X, so the parent stays open")

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{
			shadowedProvidedFallback: shadowedProvidedFallback{X: 1, Extra: map[string]int{"n": 2}},
			M:                        map[string]string{"k": "v"},
		})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":1,"k":"v"}`, string(data),
			"encoding/json splices the shallower fallback and drops the embed's")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the branch's extra-member constraint must not judge the kept fallback's members")
	})

	// The shadow marking once treated a composition nested inside a composed
	// embed as opaque, so the outer branch stayed unconditional and asserted
	// the nested embed's integer against the outer string.
	t.Run("nested composition shadowed by an outer field", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvidedNested //nolint:unused // Embedded only; exercised via reflection.

			X string `json:"X"`
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvidedNested](), provided))
		require.NoError(t, err)

		require.Len(t, s.AllOf, 1)
		require.Len(t, s.AllOf[0].AnyOf, 2, "the shadowed branch is conditional")
		assert.NotEmpty(t, s.AllOf[0].AnyOf[0].Ref)
		assert.Equal(t, &jsonschema.Schema{}, s.AllOf[0].AnyOf[1], "the escape arm is the true schema")

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{X: "str"})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":"str"}`, string(data))

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the generated schema must accept the type's own marshaled JSON")
	})

	t.Run("unshadowed embed keeps the unconditional branch", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvided //nolint:unused // Embedded only; exercised via reflection.

			Other string `json:"other"`
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"X":1,"other":"a"}`)))
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"X":"nope","other":"a"}`)),
			"a collision-free composition must stay unconditional")
	})
}

// legacyFields is an embedded struct whose fields the deprecation of the
// field that embeds it reaches, for TestGenerateEmbeddedDeprecation.
//
//nolint:unused // Embedded only; exercised via reflection.
type legacyFields struct {
	Discount int    `json:"discount,omitzero"`
	Coupon   string `json:"coupon,omitzero"   jsonschema:"deprecationMessage=coupons moved"`
	Kept     string `json:"kept,omitzero"     jsonschema:"deprecated=false"`
}

// legacyMiddle embeds legacyFields beside a field of its own, so an outer
// embed reaches both through it.
//
//nolint:unused // Embedded only; exercised via reflection.
type legacyMiddle struct {
	legacyFields `jsonschema:"deprecationMessage=the inner fields go away"`

	Mid int `json:"mid,omitzero"`
}

// legacyKind is a composed embed, whose promoted names reach the parent as
// placeholder properties beside its allOf branch.
type legacyKind struct {
	Kind string `json:"kind"`
}

// wantDeprecation is the deprecation a test expects of one property.
type wantDeprecation struct {
	message    string
	deprecated bool
}

// TestGenerateEmbeddedDeprecation pins how an embedded field that promotes
// its fields gives them its deprecation, from its doc comment or its
// jsonschema tag. A promoted field with a deprecation of its own keeps it,
// the nearest embedded field wins, and an embed under a JSON name is a
// property of its own whose deprecation stays on it. The placeholder
// property of a composed embed takes the deprecation of the embed too.
func TestGenerateEmbeddedDeprecation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func(ctx context.Context) (*jsonschema.Schema, error)
		want     map[string]wantDeprecation
		err      string
	}{
		"tag": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				type doc struct {
					legacyFields `jsonschema:"deprecationMessage=the legacy fields go away"` //nolint:unused // Embedded only; exercised via reflection.

					Name string `json:"name"`
				}

				return jsonschema.GenerateFor[doc](ctx)
			},
			want: map[string]wantDeprecation{
				"discount": {message: "the legacy fields go away", deprecated: true},
				"coupon":   {message: "coupons moved", deprecated: true},
				"kept":     {},
				"name":     {},
			},
		},
		"doc comment": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				type doc struct {
					legacyFields //nolint:unused // Embedded only; exercised via reflection.

					Name string `json:"name"`
				}

				return jsonschema.GenerateFor[doc](ctx, jsonschema.WithDescriptionProvider(
					fieldDocProvider(func(name string) string {
						if name == "legacyFields" {
							return "LegacyFields holds the old fields.\n\nDeprecated: cafe 5.0 drops them."
						}

						return ""
					}),
				))
			},
			want: map[string]wantDeprecation{
				"discount": {message: "cafe 5.0 drops them.", deprecated: true},
				"coupon":   {message: "coupons moved", deprecated: true},
				"kept":     {},
				"name":     {},
			},
		},
		"json tag without a name": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				type doc struct {
					legacyFields `json:",embed" jsonschema:"deprecated=true"` //nolint:unused // Embedded only; exercised via reflection.
				}

				return jsonschema.GenerateFor[doc](ctx)
			},
			want: map[string]wantDeprecation{
				"discount": {deprecated: true},
				"coupon":   {message: "coupons moved", deprecated: true},
			},
		},
		"json name": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				type doc struct {
					legacyFields `json:"legacy" jsonschema:"deprecationMessage=the legacy group goes away"` //nolint:unused // Embedded only; exercised via reflection.
				}

				return jsonschema.GenerateFor[doc](ctx)
			},
			want: map[string]wantDeprecation{
				"legacy": {message: "the legacy group goes away", deprecated: true},
			},
		},
		"nearest embed": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				type doc struct {
					legacyMiddle `jsonschema:"deprecationMessage=the middle fields go away"` //nolint:unused // Embedded only; exercised via reflection.
				}

				return jsonschema.GenerateFor[doc](ctx)
			},
			want: map[string]wantDeprecation{
				"mid":      {message: "the middle fields go away", deprecated: true},
				"discount": {message: "the inner fields go away", deprecated: true},
				"coupon":   {message: "coupons moved", deprecated: true},
			},
		},
		"inner embed kept current": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				//nolint:unused // Embedded only; exercised via reflection.
				type middle struct {
					legacyFields `jsonschema:"deprecated=false"` //nolint:unused // Embedded only; exercised via reflection.

					Mid int `json:"mid,omitzero"`
				}

				type doc struct {
					middle `jsonschema:"deprecationMessage=the middle fields go away"` //nolint:unused // Embedded only; exercised via reflection.
				}

				return jsonschema.GenerateFor[doc](ctx)
			},
			want: map[string]wantDeprecation{
				"mid":      {message: "the middle fields go away", deprecated: true},
				"discount": {},
			},
		},
		"composed embed": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				type doc struct {
					legacyKind `jsonschema:"deprecationMessage=kinds go away"` //nolint:unused // Embedded only; exercised via reflection.

					Name string `json:"name"`
				}

				return jsonschema.GenerateFor[doc](ctx,
					jsonschema.WithTypeSchemaFor[legacyKind](jsonschema.TypeSchema{}),
				)
			},
			want: map[string]wantDeprecation{
				"kind": {message: "kinds go away", deprecated: true},
				"name": {},
			},
		},
		"refused tag": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				type doc struct {
					legacyFields `jsonschema:"deprecationMessage="` //nolint:unused // Embedded only; exercised via reflection.
				}

				return jsonschema.GenerateFor[doc](ctx)
			},
			err: `embedded field legacyFields: jsonschema tag: key "deprecationMessage" requires a non-empty value`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate(t.Context())
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)

				return
			}

			require.NoError(t, err)

			for prop, want := range tc.want {
				require.Contains(t, s.Properties, prop, "schema: %s", marshalSchema(t, s))

				deprecated, message := deprecationOf(s.Properties[prop])
				assert.Equal(t, want.deprecated, deprecated, "property %q", prop)
				assert.Equal(t, want.message, message, "property %q", prop)
			}
		})
	}
}

// TestGenerateEmbeddedDeprecationContext pins the FieldContext a
// description provider receives for an embedded field that promotes its
// fields: the struct that declares the embed as Owner, the embedded field,
// and an empty Name, since such a field has no JSON name.
func TestGenerateEmbeddedDeprecationContext(t *testing.T) {
	t.Parallel()

	type doc struct {
		legacyFields //nolint:unused // Embedded only; exercised via reflection.

		Name string `json:"name"`
	}

	var got []jsonschema.FieldContext

	_, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.DescriptionProviderFuncs{
			FieldFunc: func(_ context.Context, fc jsonschema.FieldContext) (string, error) {
				if fc.StructField.Anonymous {
					got = append(got, fc)
				}

				return "", nil
			},
		}),
	)
	require.NoError(t, err)

	require.Len(t, got, 1, "the provider describes the embed once")
	assert.Equal(t, reflect.TypeFor[doc](), got[0].Owner)
	assert.Equal(t, "legacyFields", got[0].StructField.Name)
	assert.Equal(t, []int{0}, got[0].StructField.Index)
	assert.Empty(t, got[0].Name)
}
