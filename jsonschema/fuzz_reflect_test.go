package jsonschema_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
// `task go:fuzz` searches for new counterexamples.

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

// stringCoercion carries json:",string" on every coercible kind. The
// encoding/json package wraps each value in a JSON string, and generation must
// model each field as a string rather than by its Go kind.
type stringCoercion struct {
	Int   int     `json:"int,string"`
	Uint  uint16  `json:"uint,string"`
	Float float64 `json:"float,string"`
	Bool  bool    `json:"bool,string"`
	Str   string  `json:"str,string"`
}

// omitFields carries omitempty and omitzero. A zero value drops the field from
// the marshaled object, so generation must not require it.
type omitFields struct {
	A string    `json:"a,omitempty"`
	B int       `json:"b,omitempty"`
	C *int      `json:"c,omitempty"`
	D string    `json:"d,omitzero"`
	E time.Time `json:"e,omitzero"`
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

// rawWrapper carries a json.RawMessage, which generation models as an
// unrestricted schema and which must round-trip as arbitrary valid JSON.
type rawWrapper struct {
	Payload json.RawMessage `json:"payload"`
	Label   string          `json:"label"`
}

// bigWrapper carries a *big.Int, which marshals as a bare arbitrary-precision
// JSON number (the pointer form is the one whose pointer-receiver MarshalJSON
// encoding/json can invoke). Fuzzfill's default constructor registry knows how
// to populate big.Int, whose unexported fields defeat generic reflection.
type bigWrapper struct {
	N     *big.Int `json:"n"`
	Label string   `json:"label"`
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

// fuzzReflectAccepts is the shared body for every rig-1 target. It generates
// and compiles T's schema once, seeds the corpus, then for each blob fills a T,
// marshals it, and asserts the schema accepts the marshaled bytes. The f.Fuzz
// callback cannot call t.Parallel (the fuzzing framework forbids it), the one
// documented exemption from the t.Parallel-everywhere convention.
func fuzzReflectAccepts[T any](f *testing.F, compileOpts ...jsonschema.ValidateOption) {
	f.Helper()

	ctx := context.Background()

	schema, err := jsonschema.GenerateFor[T](ctx)
	require.NoError(f, err, "generate schema for %T", *new(T))

	validator, err := jsonschema.Compile(ctx, schema, compileOpts...)
	require.NoError(f, err, "compile schema for %T", *new(T))

	schemaJSON, err := json.MarshalIndent(schema, "", "  ")
	require.NoError(f, err, "marshal schema for %T", *new(T))

	addReflectSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		var val T

		fuzzfill.Fill(reflect.ValueOf(&val), data)

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
