package jsonschema_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math/big"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
)

// Rig 1 -- reflection vs encoding/json. The reflect.go file hand-reimplements
// the encoding/json struct-marshaling semantics, and that is where past
// fixes have clustered. The property under test closes the loop between the two
// halves of the package: for any Go value v of type T, the schema generated
// for T must accept v's own marshaled form.
//
//	Compile(GenerateFor[T]()).ValidateJSON(json.Marshal(v)) == nil
//
// A rejection means generation and marshaling disagree about T's JSON shape,
// which is a reflect.go bug. Each FuzzReflectAccepts<T> covers one roster type
// chosen to hit a historical fix cluster; fuzzfill turns the fuzzing entropy
// blob into a populated T. The seed corpus runs on every `go test`, and
// `task go:fuzz` searches for new counterexamples. This rig deliberately
// stays on default marshal options: its roster exists for the classes
// reflect.StructOf cannot express, which are orthogonal to WithJSONOptions,
// and rig 2's option-paired targets cover slices, maps, and omission far
// more densely.

// plainScalarsContainersPointers exercises the baseline kinds: scalars, a
// slice, a map, and a pointer.
type plainScalarsContainersPointers struct {
	Name   string         `json:"name"`
	Count  int            `json:"count"`
	Ratio  float64        `json:"ratio"`
	Active bool           `json:"active"`
	Tags   []string       `json:"tags"`
	Attrs  map[string]int `json:"attrs"`
	Next   *plainChild    `json:"next"`
}

type plainChild struct {
	ID   uint32 `json:"id"`
	Deep *bool  `json:"deep"`
}

// EmbeddedBase is embedded by promotion and by explicit-name roster types.
type EmbeddedBase struct {
	Base  string `json:"base"`
	Level int8   `json:"level"`
}

// embeddedPromoted promotes EmbeddedBase's fields into the outer object, the
// default encoding/json behavior for an anonymous struct field with no tag.
type embeddedPromoted struct {
	EmbeddedBase
	Extra string `json:"extra"`
}

// embeddedNamed gives the embedded field an explicit json name, so
// encoding/json does not promote it: it becomes a single named property.
type embeddedNamed struct {
	EmbeddedBase `json:"base_group"`
	Extra        string `json:"extra"`
}

// AmbiguousLeft and AmbiguousRight both promote an untagged "Shared" field at
// the same embedding depth. With neither tagged, encoding/json drops "Shared"
// entirely, and generation must drop it too or the marshaled object (which
// omits it) fails against a schema that still requires it. The fields stay
// untagged so `go vet`'s structtag check does not flag the intended collision.
type AmbiguousLeft struct {
	Shared string
	Left   string `json:"left"`
}

type AmbiguousRight struct {
	Shared string
	Right  string `json:"right"`
}

type tagTieBreakAmbiguous struct {
	AmbiguousLeft
	AmbiguousRight
}

// TaggedName and UntaggedName collide on the JSON name "Name" at the same
// depth, but only TaggedName carries an explicit tag, so encoding/json's
// tie-breaker keeps that field and generation must mirror the choice.
type TaggedName struct {
	Name string `json:"Name"`
}

type UntaggedName struct {
	Name string
}

type tagTieBreakTagWins struct {
	TaggedName
	UntaggedName
}

// ProviderObject implements JSONSchemaProvider and is embedded so it composes
// into the outer schema via allOf. Its returned schema declares the same
// property its promoted field marshals to, so the composed schema evaluates
// every key the outer value produces.
type ProviderObject struct {
	Kind string `json:"kind"`
}

func (ProviderObject) JSONSchema(context.Context, jsonschema.TypeContext) (jsonschema.TypeSchema, error) {
	return jsonschema.TypeSchema{
		Value: &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{"kind": {Type: "string"}},
		},
	}, nil
}

type providerEmbed struct {
	ProviderObject
	Extra string `json:"extra"`
}

// Celsius promotes a TextMarshaler. A struct embedding it gains the promoted
// MarshalText, so encoding/json serializes the whole struct as the marshaled
// string and generation must model the outer type as a string, not an object.
type Celsius float64

func (c Celsius) MarshalText() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(c), 'f', -1, 64)), nil
}

type promotedTextMarshaler struct {
	Celsius
	Ignored string `json:"ignored"`
}

// Money promotes a json.Marshaler. A promoted json.Marshaler can emit any JSON
// value, so generation models the outer type with an unrestricted schema.
type Money struct {
	Cents int64 `json:"cents"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	// A bare integer is valid JSON, so this emits it directly and never errors.
	return strconv.AppendInt(nil, m.Cents, 10), nil
}

type promotedJSONMarshaler struct {
	Money
	Ignored string `json:"ignored"`
}

// stringCoercion carries json:",string" on every kind the option applies to:
// encoding/json/v2 stringifies numbers only, wrapping each numeric value in a
// JSON string, and the flag survives any pointer depth. Generation must model
// each field as a string rather than by its numeric kind, and the pointer
// chains as nullable strings.
type stringCoercion struct {
	Int    int      `json:"int,string"`
	Uint   uint16   `json:"uint,string"`
	Float  float64  `json:"float,string"`
	Ptr    *int     `json:"ptr,string"`
	PtrPtr **uint32 `json:"ptrptr,string"` //nolint:staticcheck // SA5008 models v1; v2 carries the flag down the whole pointer chain.
}

// omitFields carries omitempty and omitzero. A zero value drops most of these
// fields from the marshaled object, so generation must not require them. The
// numeric omitempty fields are the exception: encoding/json/v2 never treats
// an encoded number as empty, and json.Number writes 0 for its empty value,
// so both stay required and always present.
type omitFields struct {
	A string        `json:"a,omitempty"`
	B int           `json:"b,omitempty"`
	C *int          `json:"c,omitempty"`
	D string        `json:"d,omitzero"`
	E time.Time     `json:"e,omitzero"`
	F jsonv1.Number `json:"f,omitempty"`
}

// intKeyMap marshals a map keyed by an integer, which encoding/json renders
// with the integer formatted as the JSON object key.
type intKeyMap struct {
	M map[int]string `json:"m"`
}

// timeWrapper embeds time.Time-adjacent fields to exercise the date-time
// round-trip both without and with format assertion.
type timeWrapper struct {
	When time.Time  `json:"when"`
	Opt  *time.Time `json:"opt"`
}

// rawWrapper carries a jsontext.Value, which generation models as an
// unrestricted schema and which must round-trip as arbitrary valid JSON.
type rawWrapper struct {
	Payload jsontext.Value `json:"payload"`
	Label   string         `json:"label"`
}

// bigWrapper carries a *big.Int, which marshals as a bare arbitrary-precision
// JSON number (the pointer form is the one whose pointer-receiver MarshalJSON
// encoding/json can invoke). Fuzzfill's default constructor registry knows how
// to populate big.Int, whose unexported fields defeat generic reflection.
type bigWrapper struct {
	N     *big.Int `json:"n"`
	Label string   `json:"label"`
}

// Wrap is an exported generic struct. Embedded as a concrete instantiation
// with no tag, its fields promote into the outer object like any other struct
// embed.
type Wrap[T any] struct {
	Inner T      `json:"inner"`
	Note  string `json:"note"`
}

// Bag is an exported generic map type. Encoding/json/v2 refuses an embedded
// non-struct that has no JSON name (reflect_embedded_generic_test.go pins that
// refusal), so embeddedGeneric embeds it under an explicit name and it stays a
// leaf property under that name.
type Bag[T any] map[string]T

// embeddedGeneric embeds both generic forms alongside a plain sibling: the
// struct instantiation whose fields promote, and the map instantiation that
// remains a leaf under its explicit name. Only a hand-written type can express
// this: [reflect.StructOf] cannot build an embed whose field name would have
// to be "Wrap[int]", which is not an identifier.
type embeddedGeneric struct {
	Wrap[int]
	Bag[string] `json:"bag"`

	Extra string `json:"extra"`
}

// Marker is an exported interface embedded as a leaf. Encoding/json/v2 never
// flattens an embedded interface, and refuses one that has no JSON name
// (reflect_embedded_interface_test.go pins that refusal), so embeddedInterface
// names it and it stays a regular property under that name.
type Marker interface{ MarkerKind() string }

// dualMarshaler implements json.Marshaler and encoding.TextMarshaler directly.
// Encoding/json prefers MarshalJSON, so the text form never reaches the output
// and generation must not claim type: string from MarshalText; it falls
// through to the int kind, which is what the emitted number satisfies. The
// type rides along as one field of embeddedInterface rather than carrying its
// own target, since a target of its own would fuzz a single scalar.
type dualMarshaler int

func (d dualMarshaler) MarshalJSON() ([]byte, error) {
	// A bare integer is valid JSON, so this emits it directly and never errors.
	return strconv.AppendInt(nil, int64(d), 10), nil
}

func (dualMarshaler) MarshalText() ([]byte, error) { return []byte("dual"), nil }

// embeddedInterface embeds Marker alongside fuzzable siblings. Fuzzfill leaves
// an interface at its zero value, so the embedded field marshals as
// null on every run, and the schema must both carry the property under the
// tag name and admit null there or the default additionalProperties: false
// rejects the object outright.
type embeddedInterface struct {
	Marker `json:"marker"`

	Name string        `json:"name"`
	Dual dualMarshaler `json:"dual"`
}

// DeepLeafA and DeepLeafB both promote an untagged "Overlap" two embedding
// levels below the roster type, so the collision lands at equal depth and
// encoding/json drops the name entirely. As with AmbiguousLeft and
// AmbiguousRight, the colliding fields stay untagged: `go vet`'s structtag
// check namespaces repeated json tags per embedding level, and an untagged
// field never enters that namespace, so the intended collision goes unflagged.
type DeepLeafA struct {
	Overlap string
	A       string `json:"a"`
}

// DeepLeafB is DeepLeafA's counterpart; see DeepLeafA.
type DeepLeafB struct {
	Overlap string
	B       string `json:"b"`
}

// DeepMidA relays DeepLeafA to the ambiguity depth and carries its own
// "shadowed", one level below the roster type's field of the same name and so
// the losing side of encoding/json's depth rule.
type DeepMidA struct {
	DeepLeafA

	Shadowed string `json:"shadowed"`
}

// DeepMidB only relays DeepLeafB, supplying the second half of the equal-depth
// "Overlap" collision.
type DeepMidB struct {
	DeepLeafB

	Mid int64 `json:"mid"`
}

// deepEmbedChain puts both encoding/json resolution rules on one chain two
// levels deep: the roster type's own "shadowed" wins outright over DeepMidA's
// deeper one, while "Overlap" collides untagged at equal depth and vanishes
// from the object. Generation has to reach the same verdicts, since a schema
// that keeps a dropped name requires a key no instance carries.
type deepEmbedChain struct {
	DeepMidA
	DeepMidB

	Shadowed string `json:"shadowed"`
}

func FuzzReflectAcceptsPlainStruct(f *testing.F) {
	fuzzReflectAccepts[plainScalarsContainersPointers](f)
}

func FuzzReflectAcceptsEmbeddedPromoted(f *testing.F) {
	fuzzReflectAccepts[embeddedPromoted](f)
}

func FuzzReflectAcceptsEmbeddedNamed(f *testing.F) {
	fuzzReflectAccepts[embeddedNamed](f)
}

func FuzzReflectAcceptsTagTieBreakAmbiguous(f *testing.F) {
	fuzzReflectAccepts[tagTieBreakAmbiguous](f)
}

func FuzzReflectAcceptsTagTieBreakTagWins(f *testing.F) {
	fuzzReflectAccepts[tagTieBreakTagWins](f)
}

func FuzzReflectAcceptsProviderEmbed(f *testing.F) {
	fuzzReflectAccepts[providerEmbed](f)
}

func FuzzReflectAcceptsPromotedTextMarshaler(f *testing.F) {
	fuzzReflectAccepts[promotedTextMarshaler](f)
}

func FuzzReflectAcceptsPromotedJSONMarshaler(f *testing.F) {
	fuzzReflectAccepts[promotedJSONMarshaler](f)
}

func FuzzReflectAcceptsStringCoercion(f *testing.F) {
	fuzzReflectAccepts[stringCoercion](f)
}

func FuzzReflectAcceptsOmitFields(f *testing.F) {
	fuzzReflectAccepts[omitFields](f)
}

func FuzzReflectAcceptsIntKeyMap(f *testing.F) {
	fuzzReflectAccepts[intKeyMap](f)
}

func FuzzReflectAcceptsTime(f *testing.F) {
	fuzzReflectAccepts[timeWrapper](f)
}

// FuzzReflectAcceptsTimeWithFormats additionally turns on format assertion, so
// the marshaled date-time strings must satisfy the date-time format validator,
// exercising the RFC 3339 round-trip end to end.
func FuzzReflectAcceptsTimeWithFormats(f *testing.F) {
	fuzzReflectAccepts[timeWrapper](f, jsonschema.WithFormats(true))
}

func FuzzReflectAcceptsRawMessage(f *testing.F) {
	fuzzReflectAccepts[rawWrapper](f)
}

func FuzzReflectAcceptsBigInt(f *testing.F) {
	fuzzReflectAccepts[bigWrapper](f)
}

func FuzzReflectAcceptsEmbeddedGeneric(f *testing.F) {
	fuzzReflectAccepts[embeddedGeneric](f)
}

func FuzzReflectAcceptsEmbeddedInterface(f *testing.F) {
	fuzzReflectAccepts[embeddedInterface](f)
}

func FuzzReflectAcceptsDeepEmbedChain(f *testing.F) {
	fuzzReflectAccepts[deepEmbedChain](f)
}

// fbrKey is the key type of the fallback target's map, filled through a
// constructor so a spliced member never collides with a named property, which
// would be a marshal-time duplicate-name error costing the iteration.
type fbrKey string

// embeddedFallbackMap crosses named fields with a map fallback whose value
// type is nullable, so the value schema's null branch is exercised too.
type embeddedFallbackMap struct {
	Name  string          `json:"name"`
	Count int             `json:"count,omitempty"`
	Extra map[fbrKey]*int `json:",embed"`
}

// embeddedFallbackValue carries the jsontext.Value fallback form, which rig 2
// cannot draw (its shared fill produces arbitrary JSON, and a non-object
// value is a value-level v2 refusal), so the roster owns it.
type embeddedFallbackValue struct {
	Name  string         `json:"name"`
	Extra jsontext.Value `json:",embed"`
}

// fillFbrKey draws one of eight safe fallback member names.
func fillFbrKey(c *fuzzfill.Cursor) any { return fbrKey("fbr" + strconv.Itoa(c.Intn(8))) }

// fillFallbackObject draws a jsontext.Value holding a small JSON object whose
// keys are unique and inside the safe namespace, the only content the splice
// accepts at marshal time.
func fillFallbackObject(c *fuzzfill.Cursor) any {
	out := []byte{'{'}

	n := c.Intn(4)
	for i := range n {
		if i > 0 {
			out = append(out, ',')
		}

		out = strconv.AppendQuote(out, "fbr"+strconv.Itoa(i))
		out = append(out, ':')
		out = strconv.AppendInt(out, int64(c.Intn(1000)), 10)
	}

	return jsontext.Value(append(out, '}'))
}

func FuzzReflectAcceptsEmbeddedFallbackMap(f *testing.F) {
	fuzzReflectAcceptsFilled[embeddedFallbackMap](f, []fuzzfill.Option{
		fuzzfill.WithConstructor(reflect.TypeFor[fbrKey](), fillFbrKey),
	})
}

func FuzzReflectAcceptsEmbeddedFallbackValue(f *testing.F) {
	fuzzReflectAcceptsFilled[embeddedFallbackValue](f, []fuzzfill.Option{
		fuzzfill.WithConstructor(reflect.TypeFor[jsontext.Value](), fillFallbackObject),
	})
}

// fuzzReflectAccepts is the shared body for every rig-1 target. It generates
// and compiles T's schema once, seeds the corpus, then for each blob fills a T,
// marshals it, and asserts the schema accepts the marshaled bytes. The f.Fuzz
// callback cannot call t.Parallel (the fuzzing framework forbids it), the one
// documented exemption from the t.Parallel-everywhere convention.
func fuzzReflectAccepts[T any](f *testing.F, compileOpts ...jsonschema.ValidateOption) {
	f.Helper()
	fuzzReflectAcceptsFilled[T](f, nil, compileOpts...)
}

// fuzzReflectAcceptsFilled is [fuzzReflectAccepts] with fill options, for a
// target whose type needs a constructor to stay marshalable.
func fuzzReflectAcceptsFilled[T any](
	f *testing.F,
	fillOpts []fuzzfill.Option,
	compileOpts ...jsonschema.ValidateOption,
) {
	f.Helper()

	ctx := context.Background()

	schema, err := jsonschema.GenerateFor[T](ctx)
	require.NoError(f, err, "generate schema for %T", *new(T))

	validator, err := jsonschema.Compile(ctx, schema, compileOpts...)
	require.NoError(f, err, "compile schema for %T", *new(T))

	schemaJSON := indentSchema(f, schema)

	addReflectSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		var val T

		fuzzfill.Fill(reflect.ValueOf(&val), data, fillOpts...)

		instance, err := json.Marshal(val)
		if err != nil {
			// A value fuzzfill cannot marshal is not a reflect.go finding: the
			// filler is built to stay marshalable, so this only fires on a rare
			// legitimate edge. Nothing to compare against.
			return
		}

		err = validator.ValidateJSON(ctx, instance)
		if err != nil {
			t.Fatalf(
				"schema generated for %T rejected a value of that type\n"+
					"value:    %#v\n"+
					"marshaled: %s\n"+
					"schema:   %s\n"+
					"error:    %v",
				val, val, instance, schemaJSON, err,
			)
		}
	})
}

// addReflectSeeds adds entropy blobs that reproduce the known-tricky shapes:
// the zero value (empty blob, all pointers nil), all-fields-set (high-entropy
// blobs that set every optional pointer), and mixed patterns in between.
func addReflectSeeds(f *testing.F) {
	f.Helper()
	f.Add([]byte{})
	f.Add(make([]byte, 64))
	f.Add(bytes.Repeat([]byte{0x01}, 64))
	f.Add(bytes.Repeat([]byte{0xff}, 64))
	f.Add([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10})
}
