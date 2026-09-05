package jsonprobe //nolint:testpackage // In-package by design: the agreement rig fills through the probe's own filler so both sides marshal one value; the no-in-package-test policy is main-package only.

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
	"go.jacobcolvin.com/x/jsonschema/internal/fuzzshape"
)

// panicker is a marshaler whose method must never run under the probe.
type panicker struct{ N int }

func (panicker) MarshalJSON() ([]byte, error) { panic("MarshalJSON ran under the probe") }

// textKey is a map key v2 names through its text marshaler.
type textKey struct{ A, B int }

func (k textKey) MarshalText() ([]byte, error) { return []byte("k"), nil }

// quotedNumber is a numeric kind whose marshaler v2 routes around the string
// option.
type quotedNumber int

func (quotedNumber) MarshalJSON() ([]byte, error) { return []byte(`7`), nil }

// zeroPanicker is a type whose IsZero v2 still calls under omitzero.
type zeroPanicker struct{ N int }

func (zeroPanicker) IsZero() bool { panic("IsZero ran") }

// duplicateEmbed carries the conflict v2 reports on the embedded type.
type duplicateEmbed struct {
	A int `json:"x"`
	B int `json:"x"` //nolint:govet // The duplicate json name is the fault under test.
}

// outerWithDuplicateEmbed reports the fault at the root pointer with GoType
// duplicateEmbed, the case the probe must not screen by GoType.
type outerWithDuplicateEmbed struct {
	duplicateEmbed //nolint:unused,govet // Embedded so v2 flattens its duplicate names into the outer analysis.

	C int `json:"c"`
}

func field(t *testing.T, typ reflect.Type, tag string) reflect.StructField {
	t.Helper()

	return reflect.StructField{Name: "F", Type: typ, Tag: reflect.StructTag(tag)}
}

func TestStruct(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ reflect.Type
		err error
	}{
		"plain": {
			typ: reflect.TypeFor[struct {
				A int `json:"a"`
			}](),
		},
		"empty": {typ: reflect.TypeFor[struct{}]()},
		"duplicate name": {
			typ: reflect.TypeFor[struct {
				A int `json:"x"`
				B int `json:"x"` //nolint:govet // The duplicate json name is the fault under test.
			}](),
			err: ErrDeclaration,
		},
		"duplicate name in embed": {
			typ: reflect.TypeFor[outerWithDuplicateEmbed](),
			err: ErrDeclaration,
		},
		"malformed tag": {
			typ: reflect.TypeFor[struct {
				A int `json:"a,"` //nolint:staticcheck // The trailing comma is the fault under test.
			}](),
			err: ErrDeclaration,
		},
		"tagged unexported": {
			typ: reflect.TypeFor[struct {
				a int `json:"a"` //nolint:unused,govet,staticcheck // The tag on an unexported field is the fault under test.
			}](),
			err: ErrDeclaration,
		},
		"no serializable fields": {
			typ: reflect.TypeFor[struct {
				a int //nolint:unused // an untagged unexported field is the fault under test.
			}](),
			err: ErrDeclaration,
		},
		"format option": {
			typ: reflect.TypeFor[struct {
				A time.Time `json:"a,format:RFC3339"`
			}](),
			err: ErrDeclaration,
		},
		"nested value fault is not a declaration fault": {
			typ: reflect.TypeFor[struct {
				A string `json:"a,string"`
			}](),
		},
		"nested struct fault belongs to the nested type": {
			typ: reflect.TypeFor[struct {
				A duplicateEmbed `json:"a"`
			}](),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := New(nil)

			err := p.Struct(tc.typ)
			if tc.err == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.err)
			}

			assert.Equal(t, err, p.Struct(tc.typ), "the answer must be memoized as given")
		})
	}
}

func TestField(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ         reflect.Type
		tag         string
		err         error
		stringified bool
	}{
		"int":                   {typ: reflect.TypeFor[int](), tag: `json:",string"`, stringified: true},
		"pointer to int":        {typ: reflect.TypeFor[*int](), tag: `json:",string"`, stringified: true},
		"double pointer":        {typ: reflect.TypeFor[**uint8](), tag: `json:",string"`, stringified: true},
		"six pointers":          {typ: reflect.TypeFor[******int](), tag: `json:",string"`, stringified: true},
		"float":                 {typ: reflect.TypeFor[float64](), tag: `json:",string"`, stringified: true},
		"json number":           {typ: reflect.TypeFor[jsonv1.Number](), tag: `json:",string"`, stringified: true},
		"int without string":    {typ: reflect.TypeFor[int](), tag: `json:"a"`},
		"string kind":           {typ: reflect.TypeFor[string](), tag: `json:",string"`, err: ErrValue},
		"bool kind":             {typ: reflect.TypeFor[bool](), tag: `json:",string"`, err: ErrValue},
		"pointer to string":     {typ: reflect.TypeFor[*string](), tag: `json:",string"`, err: ErrValue},
		"bytes":                 {typ: reflect.TypeFor[[]byte](), tag: `json:",string"`, err: ErrValue},
		"struct":                {typ: reflect.TypeFor[struct{ A int }](), tag: `json:",string"`, err: ErrValue},
		"time with string":      {typ: reflect.TypeFor[time.Time](), tag: `json:",string"`, err: ErrValue},
		"time":                  {typ: reflect.TypeFor[time.Time](), tag: `json:"a"`},
		"raw value":             {typ: reflect.TypeFor[jsontext.Value](), tag: `json:",string"`},
		"string kind unflagged": {typ: reflect.TypeFor[string](), tag: `json:"a"`},
		"duration":              {typ: reflect.TypeFor[time.Duration](), tag: `json:"a"`, err: ErrValue},
		"duration with string":  {typ: reflect.TypeFor[time.Duration](), tag: `json:",string"`, err: ErrValue},
		"marshaler number": {
			typ: reflect.TypeFor[quotedNumber](), tag: `json:",string"`,
		},
		"marshaler that panics": {typ: reflect.TypeFor[panicker](), tag: `json:"a"`},
		"interface":             {typ: reflect.TypeFor[any](), tag: `json:",string"`, err: ErrValue},
		"func":                  {typ: reflect.TypeFor[func()](), tag: `json:"a"`, err: ErrValue},
		"func behind six pointers": {
			typ: reflect.TypeFor[******func()](), tag: `json:"a"`, err: ErrValue,
		},
		"func omitzero": {typ: reflect.TypeFor[func()](), tag: `json:"a,omitzero"`},
		"malformed tag": {typ: reflect.TypeFor[int](), tag: `json:"a,"`, err: ErrValue},
		"zero panicker omitzero": {
			typ: reflect.TypeFor[zeroPanicker](), tag: `json:"a,omitzero"`, err: ErrValue,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := New(nil)

			stringified, err := p.Field(field(t, tc.typ, tc.tag))
			if tc.err == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.err)
			}

			assert.Equal(t, tc.stringified, stringified)
		})
	}
}

// textAlways is an integer kind whose text marshaler always writes a
// non-empty string; under the probe it writes a null instead.
type textAlways int

func (textAlways) MarshalText() ([]byte, error) { return []byte("always"), nil }

func TestOmitsZero(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ     reflect.Type
		tag     string
		err     error
		omitted bool
	}{
		"int":                 {typ: reflect.TypeFor[int](), tag: `json:"a,omitempty"`},
		"quoted int":          {typ: reflect.TypeFor[int](), tag: `json:"a,omitempty,string"`},
		"bool":                {typ: reflect.TypeFor[bool](), tag: `json:"a,omitempty"`},
		"string":              {typ: reflect.TypeFor[string](), tag: `json:"a,omitempty"`, omitted: true},
		"pointer":             {typ: reflect.TypeFor[*int](), tag: `json:"a,omitempty"`, omitted: true},
		"interface":           {typ: reflect.TypeFor[any](), tag: `json:"a,omitempty"`, omitted: true},
		"slice":               {typ: reflect.TypeFor[[]int](), tag: `json:"a,omitempty"`, omitted: true},
		"map":                 {typ: reflect.TypeFor[map[string]int](), tag: `json:"a,omitempty"`, omitted: true},
		"zero-length array":   {typ: reflect.TypeFor[[0]int](), tag: `json:"a,omitempty"`, omitted: true},
		"byte array":          {typ: reflect.TypeFor[[2]byte](), tag: `json:"a,omitempty"`},
		"time":                {typ: reflect.TypeFor[time.Time](), tag: `json:"a,omitempty"`},
		"json number":         {typ: reflect.TypeFor[jsonv1.Number](), tag: `json:"a,omitempty"`},
		"raw value":           {typ: reflect.TypeFor[jsontext.Value](), tag: `json:"a,omitempty"`, omitted: true},
		"struct with member":  {typ: reflect.TypeFor[struct{ A int }](), tag: `json:"a,omitempty"`},
		"empty struct":        {typ: reflect.TypeFor[struct{}](), tag: `json:"a,omitempty"`, omitted: true},
		"marshaler int":       {typ: reflect.TypeFor[textAlways](), tag: `json:"a,omitempty"`, omitted: true},
		"panicking marshaler": {typ: reflect.TypeFor[panicker](), tag: `json:"a,omitempty"`, omitted: true},
		"struct with marshaler member": {
			typ: reflect.TypeFor[struct{ A panicker }](), tag: `json:"a,omitempty"`,
		},
		"no option":     {typ: reflect.TypeFor[string](), tag: `json:"a"`},
		"omitzero":      {typ: reflect.TypeFor[int](), tag: `json:"a,omitzero"`, omitted: true},
		"duration":      {typ: reflect.TypeFor[time.Duration](), tag: `json:"a,omitempty"`, err: ErrValue},
		"func":          {typ: reflect.TypeFor[func()](), tag: `json:"a,omitempty"`, err: ErrValue},
		"zero panicker": {typ: reflect.TypeFor[zeroPanicker](), tag: `json:"a,omitzero"`, err: ErrValue},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := New(nil)

			omitted, err := p.OmitsZero(field(t, tc.typ, tc.tag))
			if tc.err == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.err)
			}

			assert.Equal(t, tc.omitted, omitted)

			again, err2 := p.OmitsZero(field(t, tc.typ, tc.tag))
			assert.Equal(t, omitted, again, "the answer must be memoized as given")
			assert.Equal(t, err, err2, "the answer must be memoized as given")
		})
	}
}

func TestType(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		typ reflect.Type
		err error
	}{
		"int":                 {typ: reflect.TypeFor[int]()},
		"string":              {typ: reflect.TypeFor[string]()},
		"slice":               {typ: reflect.TypeFor[[]int]()},
		"map":                 {typ: reflect.TypeFor[map[string]int]()},
		"int key":             {typ: reflect.TypeFor[map[int]int]()},
		"float key":           {typ: reflect.TypeFor[map[float64]int]()},
		"text marshaler key":  {typ: reflect.TypeFor[map[textKey]int]()},
		"pointer text key":    {typ: reflect.TypeFor[map[*textKey]int]()},
		"time":                {typ: reflect.TypeFor[time.Time]()},
		"panicking marshaler": {typ: reflect.TypeFor[panicker]()},
		"panicking marshaler ptr": {
			typ: reflect.TypeFor[*panicker](),
		},
		"func": {typ: reflect.TypeFor[func()](), err: ErrValue},
		"func behind six pointers": {
			typ: reflect.TypeFor[******func()](), err: ErrValue,
		},
		"duration behind six pointers": {
			typ: reflect.TypeFor[******time.Duration](), err: ErrValue,
		},
		"chan":           {typ: reflect.TypeFor[chan int](), err: ErrValue},
		"complex":        {typ: reflect.TypeFor[complex128](), err: ErrValue},
		"unsafe pointer": {typ: reflect.TypeFor[unsafe.Pointer](), err: ErrValue},
		"duration":       {typ: reflect.TypeFor[time.Duration](), err: ErrValue},
		"bool key":       {typ: reflect.TypeFor[map[bool]int](), err: ErrMapKey},
		"struct key":     {typ: reflect.TypeFor[map[struct{ A int }]int](), err: ErrMapKey},
		"duration key":   {typ: reflect.TypeFor[map[time.Duration]int](), err: ErrMapKey},
		"element fault belongs to the element": {
			typ: reflect.TypeFor[[]func()](),
		},
		"member fault belongs to the member": {
			typ: reflect.TypeFor[map[string]time.Duration](),
		},
		"nested struct fault belongs to the struct": {
			typ: reflect.TypeFor[[]duplicateEmbed](),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := New(nil)

			err := p.Type(tc.typ)
			if tc.err == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.err)
			}
		})
	}
}

// TestHonorsCallerOptions pins that the caller's option set reaches the
// probe: under FormatNilMapAsNull a nil map is null, which the shape of the
// output shows.
func TestConcurrentUse(t *testing.T) {
	t.Parallel()

	// Every goroutine asks one shared probe the four questions about the same
	// inputs, some of which v2 refuses, and every answer must match the one
	// a probe of its own gives. Run under -race, this also catches a data
	// race on the memos.
	type input struct {
		typ reflect.Type
		tag string
	}

	inputs := []input{
		{typ: reflect.TypeFor[int](), tag: `json:"a,string"`},
		{typ: reflect.TypeFor[string](), tag: `json:"a,omitempty"`},
		{typ: reflect.TypeFor[time.Duration](), tag: `json:"a"`},
		{typ: reflect.TypeFor[func()](), tag: `json:"a,omitempty"`},
		{typ: reflect.TypeFor[map[textKey]int](), tag: `json:"a"`},
		{typ: reflect.TypeFor[struct{ A panicker }](), tag: `json:"a,omitempty"`},
		{typ: reflect.TypeFor[struct{ N int }](), tag: `json:"a,omitempty,string"`},
	}

	// An answer renders the four verdicts as one line, so two errors built
	// by separate marshals compare by what they say.
	ask := func(p *Probe, in input) string {
		sf := reflect.StructField{Name: "F", Type: in.typ, Tag: reflect.StructTag(in.tag)}

		var decl error

		if in.typ.Kind() == reflect.Struct {
			decl = p.Struct(in.typ)
		}

		stringified, fieldErr := p.Field(sf)
		omitted, omitErr := p.OmitsZero(sf)

		return fmt.Sprintf("type=%v decl=%v field=%t/%v omit=%t/%v",
			p.Type(in.typ), decl, stringified, fieldErr, omitted, omitErr)
	}

	want := make([]string, len(inputs))
	for i, in := range inputs {
		want[i] = ask(New(nil), in)
	}

	const n = 16

	var (
		shared = New(nil)
		wg     sync.WaitGroup
		got    [n][]string
	)

	for i := range n {
		wg.Go(func() {
			for _, in := range inputs {
				got[i] = append(got[i], ask(shared, in))
			}
		})
	}

	wg.Wait()

	for i := range n {
		assert.Equal(t, want, got[i])
	}
}

func TestHonorsCallerOptions(t *testing.T) {
	t.Parallel()

	p := New(json.FormatNilMapAsNull(true))

	out, err := p.encode(reflect.ValueOf(struct{ M map[string]int }{}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"M":null}`, string(out))
}

// TestAgreesWithV2 is the agreement rig. Over the fuzzshape population, the
// probe's combined struct-and-fields verdict must match whether v2 itself
// marshals a full value of the type, with the type's own marshalers running.
func TestAgreesWithV2(t *testing.T) {
	t.Parallel()

	p := New(nil)

	fillOpts := append(fuzzshape.FillOptions(), fuzzfill.WithFull())

	for i, blob := range fuzzshape.Blobs(2048) {
		typ := fuzzshape.Type(blob)

		val := reflect.New(typ)
		fuzzfill.Fill(val, fillBlob, fillOpts...)

		_, marshalErr := json.Marshal(val.Interface())

		probeErr := p.Struct(typ)
		for sf := range typ.Fields() {
			if probeErr != nil {
				break
			}

			if !sf.IsExported() || sf.Tag.Get("json") == "-" {
				continue
			}

			_, probeErr = p.Field(sf)
		}

		if marshalErr == nil {
			assert.NoErrorf(t, probeErr, "blob %d: v2 marshals %s but the probe refuses it", i, typ)
		} else {
			assert.Errorf(t, probeErr, "blob %d: v2 refuses %s (%v) but the probe accepts it", i, typ, marshalErr)
		}
	}
}

// TestInterceptorsFallThroughForNativeCodecs pins that the native-codec
// types keep their own encoding under the probe rather than the stand-in
// null, since a stand-in would hide the string-option fault on time.Time.
func TestInterceptorsFallThroughForNativeCodecs(t *testing.T) {
	t.Parallel()

	p := New(nil)

	out, err := p.encode(reflect.ValueOf(struct {
		T time.Time
		V jsontext.Value
		N jsonv1.Number
		P panicker
	}{
		T: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
		V: jsontext.Value(`[1]`),
		N: "3",
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"T":"2020-01-02T03:04:05Z","V":[1],"N":3,"P":null}`, string(out))
}

// TestUnsupportedIsNotForwardedAsRefusal pins that a native-codec decline
// inside the interceptor never reaches the caller as a refusal.
func TestUnsupportedIsNotForwardedAsRefusal(t *testing.T) {
	t.Parallel()

	p := New(nil)

	err := p.Type(reflect.TypeFor[[]time.Time]())
	require.NoError(t, err)
}
