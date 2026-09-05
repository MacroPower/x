package jsonschema_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
	"go.jacobcolvin.com/x/jsonschema/internal/reflectkind"
)

// FuzzParseSchemaValueExactRepair searches for a document whose float64-leaf
// variant parses differently through ParseSchemaValue's direct exact copy
// than through the marshal round trip it replaced. Plain v2 decoding gives
// the variant float64 leaves, the shape whose repair depends on the exact
// copy rendering the same shortest-decimal literal the encoder writes.
func FuzzParseSchemaValueExactRepair(f *testing.F) {
	f.Add([]byte(`true`))
	f.Add([]byte(`{"const":9007199254740993}`))
	f.Add([]byte(`{"enum":[0.1,1,"s",null]}`))
	f.Add([]byte(`{"examples":[{"const":0.1}]}`))
	f.Add([]byte(`{"myext":{"const":9007199254740993},"$ref":"#/myext"}`))
	f.Add([]byte(`{"multipleOf":1e-320}`))
	f.Add([]byte(`{"properties":{"a":{"enum":[1e21,1e-9,-0.0]}}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var doc any

		err := json.Unmarshal(data, &doc)
		if err != nil {
			t.Skip("not a JSON document")
		}

		switch doc.(type) {
		case map[string]any, bool:
		default:
			t.Skip("not a schema document shape")
		}

		assertParseValueMatchesRemarshal(t, doc)
	})
}

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

// Rig 2 -- reflection vs encoding/json, over a synthesized type shape. Rig 1
// asserts the same property over a hand-written roster, so it only catches
// drift in shapes someone already thought to write down. This rig fuzzes the
// type shape as well as the value:
//
//	Compile(Generate(fuzzshape.Type(shape))).ValidateJSON(json.Marshal(v)) == nil
//
// The callback takes two blobs so shape and value entropy evolve
// independently; the fuzzing engine mutates each on its own. That arity means
// rig 1's addReflectSeeds cannot be reused, since f.Add panics unless its
// arity matches the callback's.
//
// FuzzShapeRejectsNearMiss is the complement: a schema that accepts everything
// would satisfy the accept rigs trivially, so it asserts that instances just
// outside the type's marshaled shape are refused.

// Three causes would make a synthesized shape report a divergence the package
// is not guilty of. Each has a reason constant carried as the message of a
// guard that pins the cause down, so an exclusion cannot quietly stop being
// true. The two below are the rig's own: one explains a variant the rig
// deliberately does not have, the other why a coverage class stays in rig 1's
// roster. The third constrains fuzzshape's pools and lives with the guard over
// them, as reasonUnmodeledMarshaler in internal/fuzzshape.

// reasonStructOfPromotion is why the promoted-marshaler and embedded-generic
// classes stay in rig 1's static roster, where the compiler builds the
// promotion wrappers for real. It describes a limitation of runtime type
// construction rather than package behavior, and so is deliberately absent from
// doc.go and README.md: documenting it publicly would describe the package as
// behaving in a way it does not.
const reasonStructOfPromotion = "reflect.StructOf cannot reproduce method promotion: a synthesized embed's promoted method reports the embedded type's real source file rather than the <autogenerated> marker reflectkind.HasDirectMethod keys on, so it is misclassified as direct. It also cannot name a generic instantiation, since Wrap[string] is not a valid Go field name"

// reasonFallbackAdmitsExtras is why FuzzShapeRejectsNearMiss skips its
// extra-property leg when the drawn shape carries a winning embedded
// fallback. TestShapeFallbackNearMissTeeth restores the leg's teeth
// deterministically.
const reasonFallbackAdmitsExtras = "an embedded fallback's value schema admits unknown members by design, so an injected sentinel property is not a near miss; the schema still constrains each member's value"

// nearMissSentinel is the property name FuzzShapeRejectsNearMiss injects. No
// fuzzshape tag pool entry can produce it, so its absence from a marshaled
// instance is guaranteed; the rig asserts that anyway rather than trusting it.
const nearMissSentinel = "__fuzzshape_sentinel__"

// TestShapeStructOfCannotReproducePromotion pins down reasonStructOfPromotion.
// A statically declared embed's promoted method carries the <autogenerated>
// source location reflectkind keys on; the same embed built by reflect.StructOf
// carries the embedded type's real source file and so reads as directly
// declared. That inversion is why promoted-marshaler coverage stays in rig 1's
// roster. Were Go ever to make StructOf reproduce promotion, this fails and the
// coverage could move.
func TestShapeStructOfCannotReproducePromotion(t *testing.T) {
	t.Parallel()

	declared := reflect.TypeFor[promotedTextMarshaler]()
	require.True(t, reflectkind.ImplementsAnyTextMarshaler(declared) &&
		!reflectkind.HasDirectMethod(declared, "MarshalText"),
		"a declared embed must read as promoted")

	synthesized := reflect.StructOf([]reflect.StructField{
		{Name: "Celsius", Type: reflect.TypeFor[Celsius](), Anonymous: true},
	})
	require.True(t, reflectkind.ImplementsAnyTextMarshaler(synthesized) &&
		reflectkind.HasDirectMethod(synthesized, "MarshalText"), reasonStructOfPromotion)

	// The reason's other half: an embedded generic instantiation cannot be named
	// at all, since a field name must be an identifier.
	require.Panics(t, func() {
		reflect.StructOf([]reflect.StructField{
			{Name: "Wrap[int]", Type: reflect.TypeFor[Wrap[int]](), Anonymous: true},
		})
	}, reasonStructOfPromotion)
}

func FuzzShapeAccepts(f *testing.F) {
	fuzzShapeAccepts(f, nil, nil)
}

// FuzzShapeAcceptsDraft7 targets Draft-07, whose $ref and definitions handling
// differs enough from Draft 2020-12 to reach different generation paths.
func FuzzShapeAcceptsDraft7(f *testing.F) {
	fuzzShapeAccepts(f, []jsonschema.GenerateOption{jsonschema.WithDraft(jsonschema.Draft7)}, nil)
}

// FuzzShapeAcceptsOpenObjects drops additionalProperties: false. The property
// still has teeth: an open object only stops rejecting unknown keys, and every
// declared property's own schema still has to admit what the field marshals to.
func FuzzShapeAcceptsOpenObjects(f *testing.F) {
	fuzzShapeAccepts(f, []jsonschema.GenerateOption{jsonschema.WithAdditionalProperties(true)}, nil)
}

// FuzzShapeAcceptsNoDefs inlines every named type instead of extracting it to
// $defs, so the pooled named structs are reflected at each use site rather than
// once behind a $ref.
func FuzzShapeAcceptsNoDefs(f *testing.F) {
	fuzzShapeAccepts(f, []jsonschema.GenerateOption{jsonschema.WithDefinitions(false)}, nil)
}

// FuzzShapeAcceptsWithFormats turns on format assertion, so the pool's
// time.Time fields must satisfy the date-time format validator, the same RFC
// 3339 round-trip rig 1 covers with timeWrapper.
func FuzzShapeAcceptsWithFormats(f *testing.F) {
	fuzzShapeAccepts(f, nil, []jsonschema.ValidateOption{jsonschema.WithFormats(true)})
}

// FuzzShapeAcceptsNilContainersNull pairs WithJSONOptions(FormatNil*AsNull)
// generation with the same marshal options and the nil-container fill draw;
// without that draw the default fill never produces a nil slice or map and
// the target would assert nothing new.
func FuzzShapeAcceptsNilContainersNull(f *testing.F) {
	opts := []json.Options{json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true)}
	fuzzShapeAcceptsOpts(f,
		[]jsonschema.GenerateOption{jsonschema.WithJSONOptions(opts...)}, nil,
		opts, []fuzzfill.Option{fuzzfill.WithNilContainers()})
}

// FuzzShapeAcceptsOmitZero pairs WithJSONOptions(OmitZeroStructFields)
// generation with the matching marshal option; the zero-heavy seed blobs
// exercise omission with the default fill.
func FuzzShapeAcceptsOmitZero(f *testing.F) {
	opts := []json.Options{json.OmitZeroStructFields(true)}
	fuzzShapeAcceptsOpts(f,
		[]jsonschema.GenerateOption{jsonschema.WithJSONOptions(opts...)}, nil, opts, nil)
}

// fuzzShapeAccepts is the shared body for every rig-2a target. Unlike rig 1 it
// generates and compiles inside the callback, since the type differs per
// iteration. The f.Fuzz callback cannot call t.Parallel (the fuzzing framework
// forbids it), the one documented exemption from the t.Parallel-everywhere
// convention.
func fuzzShapeAccepts(
	f *testing.F,
	genOpts []jsonschema.GenerateOption,
	validateOpts []jsonschema.ValidateOption,
) {
	f.Helper()
	fuzzShapeAcceptsOpts(f, genOpts, validateOpts, nil, nil)
}

// fuzzShapeAcceptsOpts is fuzzShapeAccepts with the marshal side configured
// in lockstep: an options-paired target (WithJSONOptions on the generation
// side) marshals its instances under the same encoding/json/v2 options, and
// may add fill draws the property needs (the nil-container target is
// toothless without nil containers). The refusal probe deliberately stays on
// default marshal options: generation's refusals are declaration-level and
// option-independent, while a shape-altering option can hide the faulty
// declaration from v2 before it is evaluated (OmitZeroStructFields omits a
// zero-valued field whose saturated fill is still zero -- an opaque type --
// so its invalid ,string tag never errs), which would break the agreement
// without any disagreement about the declaration.
func fuzzShapeAcceptsOpts(
	f *testing.F,
	genOpts []jsonschema.GenerateOption,
	validateOpts []jsonschema.ValidateOption,
	marshalOpts []json.Options,
	fillOpts []fuzzfill.Option,
) {
	f.Helper()

	ctx := context.Background()

	addShapeSeeds(f)

	f.Fuzz(func(t *testing.T, shape, values []byte) {
		rt := fuzzshape.Type(shape)
		schema, validator := generateAndCompile(ctx, t, rt, genOpts, validateOpts)
		instance := marshalShapeValue(t, rt, values, marshalOpts, fillOpts)

		err := validator.ValidateJSON(ctx, instance)
		if err != nil {
			t.Fatalf(
				"schema generated for a synthesized type rejected a value of that type\n"+
					"type:      %s\n"+
					"marshaled: %s\n"+
					"schema:    %s\n"+
					"error:     %v",
				rt, instance, indentSchema(t, schema), err,
			)
		}
	})
}

// FuzzShapeRejectsNearMiss asserts the generated schema is not vacuous. It runs
// on the default variant, where the object is closed, and builds two kinds of
// near miss from an instance the schema just accepted: an instance carrying one
// extra property, and an instance missing one required property.
//
// No draft or allOf gate is needed. AllOf composition fires only from
// needsAllOfComposition, which requires a registered TypeSchemaProvider, a
// WithTypeSchema override, or a JSONSchemaProvider; fuzzshape's pools are
// provider-free by construction, so no synthesized shape reaches it, and
// buildStructSchema therefore closes the object unless the shape carries a
// winning embedded fallback, whose extra-property leg the target skips
// (reasonFallbackAdmitsExtras). A StructOf root is inlined rather than
// extracted to $defs, so the root's Required list is readable directly.
func FuzzShapeRejectsNearMiss(f *testing.F) {
	ctx := context.Background()

	addShapeSeeds(f)

	f.Fuzz(func(t *testing.T, shape, values []byte) {
		rt := fuzzshape.Type(shape)
		schema, validator := generateAndCompile(ctx, t, rt, nil, nil)
		instance := marshalShapeValue(t, rt, values, nil, nil)

		// A StructOf root always marshals to an object, since the pools exclude
		// every embed that could promote a marshaler. Asserting it rather than
		// skipping keeps a pool change from turning the rig into a no-op.
		var object map[string]jsontext.Value

		require.NoErrorf(t, json.Unmarshal(instance, &object),
			"a value of %s marshaled to %s, which is not an object", rt, instance)

		// Unmarshaling null into a map succeeds and leaves it nil, so the error
		// check alone would let a null root reach the mutations below. Null is
		// exactly what a marshaler-promoting embed would produce.
		require.NotNilf(t, object, "a value of %s marshaled to null, not an object", rt)

		require.NotContainsf(t, object, nearMissSentinel,
			"the sentinel property is already present in %s, so injecting it is not a near miss", instance)

		if !shapeHasFallback(rt) {
			extra := maps.Clone(object)
			extra[nearMissSentinel] = jsontext.Value(`1`)
			requireRejects(ctx, t, validator, rt, schema, extra, "an unknown property")
		} else {
			t.Log(reasonFallbackAdmitsExtras)
		}

		for _, name := range schema.Required {
			require.Containsf(t, object, name,
				"schema generated for %s requires %q, which the marshaled instance %s does not carry",
				rt, name, instance)

			missing := maps.Clone(object)
			delete(missing, name)
			requireRejects(ctx, t, validator, rt, schema, missing, "a missing required property "+name)
		}
	})
}

// shapeHasFallback reports whether a drawn shape carries a winning embedded
// fallback. The pools draw fallbacks at depth 0 only (no embed pool type
// carries one), and a shape that drew two is a declaration refusal
// generateAndCompile already skipped, so a depth-0 scan for a non-anonymous
// ",embed" field of a qualifying type is exact.
func shapeHasFallback(rt reflect.Type) bool {
	for field := range rt.Fields() {
		if field.Anonymous || !strings.Contains(field.Tag.Get("json"), ",embed") {
			continue
		}

		ft := field.Type
		if ft.Kind() == reflect.Pointer && ft.Name() == "" {
			ft = ft.Elem()
		}

		if reflectkind.IsEmbeddedFallback(ft) {
			return true
		}
	}

	return false
}

// TestShapeFallbackNearMissTeeth pins reasonFallbackAdmitsExtras and restores
// the teeth the skip gives up: the fallback's value schema still judges every
// extra member, accepting one of the value type and rejecting one outside it.
func TestShapeFallbackNearMissTeeth(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	schema, err := jsonschema.GenerateFor[fbMap](ctx)
	require.NoError(t, err)

	validator, err := jsonschema.Compile(ctx, schema)
	require.NoError(t, err)

	require.NoError(t, validator.ValidateJSON(ctx, []byte(`{"name":"n","extra":7}`)),
		"an extra integer member satisfies the fallback's value schema")
	require.Error(t, validator.ValidateJSON(ctx, []byte(`{"name":"n","extra":"seven"}`)),
		reasonFallbackAdmitsExtras)
}

// requireRejects marshals a mutated object and fails unless the schema refuses
// it.
func requireRejects(
	ctx context.Context,
	t *testing.T,
	validator *jsonschema.Validator,
	rt reflect.Type,
	schema *jsonschema.Schema,
	object map[string]jsontext.Value,
	what string,
) {
	t.Helper()

	mutated, err := json.Marshal(object)
	require.NoErrorf(t, err, "marshal the near-miss instance for %s", rt)

	err = validator.ValidateJSON(ctx, mutated)
	if err == nil {
		t.Fatalf(
			"schema generated for a synthesized type accepted an instance carrying %s\n"+
				"type:      %s\n"+
				"near miss: %s\n"+
				"schema:    %s",
			what, rt, mutated, indentSchema(t, schema),
		)
	}
}

// generateAndCompile builds the schema for a synthesized type and compiles it,
// the step every rig-2 target opens with. Compiling runs the compile-time
// structure, identifier, and reference checks, so it doubles as the
// structural well-formedness assertion; full metaschema validation stays in
// conformance_test.go, too costly to run per iteration.
//
// A refused generation is not automatically a failure: the pools draw
// declarations encoding/json/v2 itself refuses (a tag with a cut name, a
// ",string" on a non-numeric field, an unnamed embedded non-struct, a struct
// with no serializable fields), and the ground-truth property then becomes
// agreement on the refusal. Generate may err only where v2 refuses to marshal the same type,
// asserted on a saturated fill so a nil pointer cannot hide the faulty
// declaration; the target then skips, having asserted the agreement.
func generateAndCompile(
	ctx context.Context,
	t *testing.T,
	rt reflect.Type,
	genOpts []jsonschema.GenerateOption,
	validateOpts []jsonschema.ValidateOption,
) (*jsonschema.Schema, *jsonschema.Validator) {
	t.Helper()

	schema, err := jsonschema.Generate(ctx, rt, genOpts...)
	if err != nil {
		requireV2RefusesValue(t, rt, err)
		t.Skipf("declaration refused by generation and encoding/json/v2 alike: %v", err)
	}

	validator, err := jsonschema.Compile(ctx, schema, validateOpts...)
	require.NoErrorf(t, err, "compile the schema generated for %s", rt)

	return schema, validator
}

// marshalShapeValue fills a value of rt from the value blob and marshals it. A
// marshal error is a failure rather than a skip, unlike rig 1's: fuzzshape's
// component pool is marshalable by construction and declares its own filling
// requirements through FillOptions, so a failure here means the pool or a
// constructor is wrong, not that the fuzzer found a hostile value.
func marshalShapeValue(
	t *testing.T, rt reflect.Type, values []byte,
	marshalOpts []json.Options, fillOpts []fuzzfill.Option,
) []byte {
	t.Helper()

	val := reflect.New(rt)
	fuzzfill.Fill(val, values, append(fuzzshape.FillOptions(), fillOpts...)...)

	instance, err := json.Marshal(val.Interface(), marshalOpts...)
	require.NoErrorf(t, err, "marshal a value of %s", rt)

	return instance
}

// requireV2RefusesValue asserts encoding/json/v2 also refuses to marshal a
// value of rt, the agreement a refused generation pins. The fill is full so
// neither a nil pointer nor an empty container can hide the faulty
// declaration behind a null, [] or {}.
func requireV2RefusesValue(tb testing.TB, rt reflect.Type, genErr error) {
	tb.Helper()

	val := reflect.New(rt)
	fuzzfill.Fill(val, bytes.Repeat([]byte{0xff}, 512),
		append(fuzzshape.FillOptions(), fuzzfill.WithFull())...)

	_, err := json.Marshal(val.Interface())
	require.Errorf(tb, err,
		"generation refused %s (%v) but encoding/json/v2 marshals it", rt, genErr)
}

// indentSchema renders a schema for a failure message. It runs only on the
// failure path, since the schema differs per iteration and rendering every one
// would cost more than the property check itself.
func indentSchema(tb testing.TB, schema *jsonschema.Schema) []byte {
	tb.Helper()

	rendered, err := json.Marshal(schema, jsontext.WithIndent("  "))
	require.NoError(tb, err, "marshal the generated schema for the failure message")

	return rendered
}

// shapeSeedDraws indexes the shape blobs the seed corpus draws from
// [fuzzshape.Blobs]. The seeds are the only part of rig 2 the fast gate runs,
// so they are chosen rather than arbitrary: the byte patterns one reaches for
// by hand -- all zeros, all ones, a repeating nibble -- drive every draw to the
// same residue, which collapses the shape to at most one field once the name
// dedupe runs. TestShapeSeedsDrawRichShapes states what the set has to cover.
var shapeSeedDraws = []int{79, 85, 162, 372, 1976}

// seedShapeBlobs returns the population shapeSeedDraws indexes into.
func seedShapeBlobs() [][]byte {
	return fuzzshape.Blobs(slices.Max(shapeSeedDraws) + 1)
}

// addShapeSeeds seeds a rig-2 target. It pairs each shape blob with a value
// blob alternating the two ends of the value axis: zeroed leaves every pointer
// nil, saturated sets them.
func addShapeSeeds(f *testing.F) {
	f.Helper()

	// The field-free struct, whose schema closes an object with no properties.
	f.Add([]byte{}, []byte{})

	blobs := seedShapeBlobs()
	for i, draw := range shapeSeedDraws {
		values := bytes.Repeat([]byte{0xff}, 96)
		if i%2 == 0 {
			values = make([]byte, 96)
		}

		f.Add(blobs[draw], values)
	}
}

// TestShapeSeedsDrawRichShapes asserts the seed corpus is worth running. A
// corpus drawing only thin shapes would leave every target passing while
// covering nothing, and silently, since a thin shape is still a valid one.
// Asserting properties of the draws makes that loud, and the Required check is
// what gives FuzzShapeRejectsNearMiss's Required loop a body on the gate.
//
// The two embed classes are the ones a synthesized rig is best placed to fuzz
// and the reason the set is this size: encoding/json drops a name two embeds
// promote at equal depth, and lets an outer field shadow a deeper one. Both are
// read off the embedded type's own generated schema rather than re-derived
// here, so the guard cannot disagree with the resolution rules under test.
//
// The indices are positions in a generated population, so any change to the
// pools or the draw odds re-rolls what they name; this guard is what forces
// them to be re-chosen instead of quietly going thin.
func TestShapeSeedsDrawRichShapes(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	blobs := seedShapeBlobs()

	fieldClasses := map[string]func(reflect.StructField) bool{
		"an embedded field": func(f reflect.StructField) bool {
			return f.Anonymous
		},
		"an unexported field": func(f reflect.StructField) bool {
			return !f.IsExported()
		},
		"a ,string coercion": func(f reflect.StructField) bool {
			return strings.Contains(f.Tag.Get("json"), ",string")
		},
		"a tag naming a property": func(f reflect.StructField) bool {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")

			return name != "" && name != "-"
		},
		"an embedded fallback": func(f reflect.StructField) bool {
			return !f.Anonymous && strings.Contains(f.Tag.Get("json"), ",embed")
		},
		"a format tag option": func(f reflect.StructField) bool {
			return strings.Contains(f.Tag.Get("json"), ",format:")
		},
	}

	seen := make(map[string]bool, len(fieldClasses)+2)

	for _, draw := range shapeSeedDraws {
		rt := fuzzshape.Type(blobs[draw])
		require.GreaterOrEqualf(t, rt.NumField(), 3, "seed draw %d yields the thin shape %s", draw, rt)

		schema, err := jsonschema.Generate(ctx, rt)
		if err != nil {
			// A refused draw earns its seed slot only through the refusal
			// half of the property, v2 refusing the same declaration. The
			// format-option class is reachable no other way.
			requireV2RefusesValue(t, rt, err)
		} else {
			require.NotEmptyf(t, schema.Required, "seed draw %d (%s) requires no property", draw, rt)
			markPromotionClasses(ctx, t, rt, schema, seen)
		}

		for field := range rt.Fields() {
			for name, match := range fieldClasses {
				if match(field) {
					seen[name] = true
				}
			}
		}
	}

	for name := range fieldClasses {
		require.Truef(t, seen[name], "no seed draw carries %s", name)
	}

	require.True(t, seen[droppedClass], "no seed draw drops a name two embeds promote at equal depth")
	require.True(t, seen[shadowedClass], "no seed draw shadows a promoted name with an outer field")
}

// Names of the two embed-resolution classes markPromotionClasses records.
const (
	droppedClass  = "a promoted name dropped at equal depth"
	shadowedClass = "a promoted name shadowed by an outer field"
)

// markPromotionClasses records which embed-resolution rules a drawn shape
// exercises, by comparing each untagged struct embed's own generated schema
// against the root's: a promoted property missing from the root was dropped,
// and one carrying a different type was shadowed by an outer field.
func markPromotionClasses(
	ctx context.Context,
	t *testing.T,
	rt reflect.Type,
	schema *jsonschema.Schema,
	seen map[string]bool,
) {
	t.Helper()

	for field := range rt.Fields() {
		// A name part is what suppresses promotion, so an options-only tag on an
		// embed still promotes and must be inspected.
		tagName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.Anonymous || tagName != "" {
			continue
		}

		embedded := field.Type
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}

		if embedded.Kind() != reflect.Struct {
			continue
		}

		promoted, err := jsonschema.Generate(ctx, embedded)
		require.NoErrorf(t, err, "generate a schema for the embedded %s", embedded)

		for name, sub := range promoted.Properties {
			at, ok := schema.Properties[name]
			switch {
			case !ok:
				seen[droppedClass] = true
			case at.Type != sub.Type:
				seen[shadowedClass] = true
			}
		}
	}
}
