// Package jsonprobe asks [encoding/json/v2] whether it refuses a Go type, a
// struct declaration, or a struct field, and reports the verdict as a sentinel
// the generator maps onto its own errors.
//
// The oracle is a real marshal. [Probe.Struct] marshals the zero value of a
// struct, which is enough to trip every declaration fault v2 raises before it
// writes the first token (a conflicting name, a malformed tag, a tagged
// unexported field, a struct with fields but none serializable). [Probe.Field]
// and [Probe.Type] marshal a filled value, since v2 raises a value-level fault
// (a `string` option on a kind that encodes no number, a [time.Duration] with
// no format, an unsupported kind, a map key it cannot name) only when it
// writes the value, and a nil pointer or an empty container hides the
// element type behind a null, [] or {}. [Probe.OmitsZero] marshals a field's
// zero value, the emptiest value its type encodes, and reads whether the
// omitempty option dropped the member.
//
// User code never runs. Four interceptors registered through
// [encoding/json/v2.WithMarshalers] catch every type whose method set carries
// a marshal interface and write a null in its place (an empty-name string in
// object-name position, where a null is itself a fault), so a method that
// panics, blocks, or answers differently per value cannot decide a verdict.
// The three types v2 encodes through a native codec that outranks their
// methods ([time.Time], [jsontext.Value], and [encoding/json.Number]) fall
// through to that codec. The one user method v2 still calls is IsZero under
// the omitzero option, so every marshal runs under a recover and a panic
// reports as [ErrValue].
package jsonprobe

import (
	"bytes"
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	jsonv1 "encoding/json"

	"go.jacobcolvin.com/x/jsonschema/internal/fuzzfill"
)

var (
	// ErrDeclaration reports a struct whose declaration v2 refuses before
	// writing its first token.
	ErrDeclaration = errors.New("struct declaration refused by encoding/json/v2")

	// ErrValue reports a type or field whose filled value v2 refuses to
	// marshal.
	ErrValue = errors.New("value refused by encoding/json/v2")

	// ErrMapKey reports a map whose filled key v2 cannot encode as an object
	// member name. An interface-kind key fills as nil and is refused here,
	// though v2 accepts the map at run time when every key holds a string.
	ErrMapKey = errors.New("map key refused by encoding/json/v2")

	// The nativeCodecs set holds the types v2 encodes through its own codec
	// even though their method sets carry a marshal interface. The
	// interceptors decline them so the verdict on each is the codec's own.
	nativeCodecs = map[reflect.Type]bool{
		reflect.TypeFor[time.Time]():      true,
		reflect.TypeFor[jsontext.Value](): true,
		reflect.TypeFor[jsonv1.Number]():  true,
	}

	// The fillBlob bytes are the entropy every filled value draws its scalars
	// from. The value's shape does not depend on it under [fuzzfill.WithFull].
	fillBlob = bytes.Repeat([]byte{0x5a, 0xa5, 0x3c}, 512)

	// The fillOptions make every filled value expose its whole declaration,
	// and keep a [encoding/json.Number] a valid literal, which the generic
	// string fill would not.
	fillOptions = []fuzzfill.Option{
		fuzzfill.WithFull(),
		fuzzfill.WithConstructor(reflect.TypeFor[jsonv1.Number](), func(*fuzzfill.Cursor) any {
			return jsonv1.Number("1")
		}),
	}
)

// fieldResult is one memoized [Probe.Field] answer.
type fieldResult struct {
	err         error
	stringified bool
}

// omitResult is one memoized [Probe.OmitsZero] answer.
type omitResult struct {
	err     error
	omitted bool
}

// Probe answers refusal questions for one set of marshal options. It memoizes
// every answer and is not safe for concurrent use; a generation run owns one.
// The field verdicts are keyed by the one-field struct type [oneField]
// builds, which [reflect.StructOf] interns per field type and tag, the two
// inputs that decide one.
type Probe struct {
	opts    json.Options
	types   map[reflect.Type]error
	structs map[reflect.Type]error
	fields  map[reflect.Type]fieldResult
	omits   map[reflect.Type]omitResult
}

// New returns a Probe whose verdicts are v2's under opts joined with the
// interceptors. Opts is the caller's own option set and carries no marshalers
// of its own; a nil opts means the defaults.
func New(opts json.Options) *Probe {
	marshalers := json.JoinMarshalers(
		intercept[json.MarshalerTo](),
		intercept[json.Marshaler](),
		intercept[encoding.TextAppender](),
		intercept[encoding.TextMarshaler](),
	)

	joined := json.WithMarshalers(marshalers)
	if opts != nil {
		joined = json.JoinOptions(opts, joined)
	}

	return &Probe{
		opts:    joined,
		types:   map[reflect.Type]error{},
		structs: map[reflect.Type]error{},
		fields:  map[reflect.Type]fieldResult{},
		omits:   map[reflect.Type]omitResult{},
	}
}

// intercept builds the marshaler that stands in for every type whose pointer
// implements T. It declines the native-codec types with
// [errors.ErrUnsupported] and no token written, which v2 treats as a
// fall-through to the next arshaler.
func intercept[T any]() *json.Marshalers {
	return json.MarshalToFunc(func(enc *jsontext.Encoder, v T) error {
		// V2 hands an interface-typed func a non-nil pointer to the concrete
		// value, so one Elem reaches the type whose methods matched.
		t := reflect.TypeOf(v)
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}

		if nativeCodecs[t] {
			return errors.ErrUnsupported
		}

		if inNamePosition(enc) {
			// A null is not a member name, and two equal names are a
			// duplicate-name fault, so the stand-in name is the unique
			// output offset.
			return enc.WriteToken(jsontext.String(strconv.FormatInt(enc.OutputOffset(), 10)))
		}

		return enc.WriteToken(jsontext.Null)
	})
}

// inNamePosition reports whether the encoder's next token is an object member
// name: the innermost open container is an object with an even number of
// tokens written so far.
func inNamePosition(enc *jsontext.Encoder) bool {
	kind, length := enc.StackIndex(enc.StackDepth())

	return kind == '{' && length%2 == 0
}

// Struct reports whether v2 refuses the declaration of struct type t, as
// [ErrDeclaration] wrapping v2's own reason. It marshals the zero value: v2
// analyzes a struct's fields before it writes the opening brace, and a fault
// found there carries an empty JSON pointer, which is what separates it from a
// fault inside a nested field's value. A fault inside an embedded struct
// reports the same way, since v2 flattens embedded fields into the outer
// analysis. The caller skips a type whose method set carries a marshal
// interface, whose fields v2 never analyzes.
func (p *Probe) Struct(t reflect.Type) error {
	if err, ok := p.structs[t]; ok {
		return err
	}

	err := p.declaration(reflect.Zero(t))
	p.structs[t] = err

	return err
}

// declaration marshals v and reports a fault v2 raised at the root as
// [ErrDeclaration].
func (p *Probe) declaration(v reflect.Value) error {
	_, err := p.encode(v)

	var serr *json.SemanticError

	if !errors.As(err, &serr) || serr.JSONPointer != "" {
		return nil
	}

	return fmt.Errorf("%w: %w", ErrDeclaration, cause(err))
}

// Field reports whether v2 refuses a struct field declared with sf's type and
// tag, and whether the tag's `string` option is what makes the written value
// a JSON string. It probes a one-field struct built from sf with the embedded
// flag cleared, filled through [fuzzfill.WithFull] so no pointer level or
// container hides the leaf. A fault of any kind is [ErrValue] wrapping v2's
// reason. The field reads as stringified when the tagged marshal writes a
// string where an untagged marshal of the same value does not: the integer
// and float kinds and a [encoding/json.Number] do at any pointer depth. A
// type whose method set carries a marshal interface writes a null under the
// interceptors either way, matching v2 ignoring the option on such a type,
// and a [jsontext.Value] writes its own bytes either way.
func (p *Probe) Field(sf reflect.StructField) (bool, error) {
	tagged := oneField(sf)

	res, ok := p.fields[tagged]
	if !ok {
		res = p.probeField(tagged, sf.Type)
		p.fields[tagged] = res
	}

	return res.stringified, res.err
}

// probeField answers [Probe.Field] for the one-field struct tagged, whose
// field has type typ.
func (p *Probe) probeField(tagged, typ reflect.Type) fieldResult {
	out, err := p.encode(p.filled(tagged))
	if err != nil {
		return fieldResult{err: fmt.Errorf("%w: %w", ErrValue, cause(err))}
	}

	if !firstMemberIsString(out) {
		return fieldResult{}
	}

	// The untagged struct fills at the same depth, so both marshals see one
	// value. A tag option can hide a fault the untagged marshal raises
	// (omitzero on a func), and the tagged verdict rules, so that fault only
	// reads as not stringified.
	untagged := oneField(reflect.StructField{Type: typ})

	bare, err := p.encode(p.filled(untagged))
	if err != nil {
		return fieldResult{}
	}

	return fieldResult{stringified: !firstMemberIsString(bare)}
}

// OmitsZero reports whether v2 omits a struct field declared with sf's type
// and tag when the field holds its zero value, which is whether the tag's
// omitempty option ever omits the field. Omitempty drops a member whose
// encoded value is null, "", {}, or [], and the zero value encodes the
// emptiest value a type has: a nil pointer, interface, slice, or map, an
// empty string, and a struct with every member at its own zero. The three
// native-codec types answer as their codecs write, so a [time.Time] (its
// zero instant) and a [encoding/json.Number] (0) never omit, while a nil
// [jsontext.Value] does. A type whose method set carries a marshal
// interface writes a null under the interceptors, at the field or inside a
// struct member, and so reads as omitted whatever its method would write; a
// schema that leaves such a field optional accepts every document v2
// writes. A fault of any kind is [ErrValue] wrapping v2's reason.
func (p *Probe) OmitsZero(sf reflect.StructField) (bool, error) {
	tagged := oneField(sf)

	res, ok := p.omits[tagged]
	if !ok {
		res = p.probeOmitsZero(tagged)
		p.omits[tagged] = res
	}

	return res.omitted, res.err
}

// probeOmitsZero answers [Probe.OmitsZero] for the one-field struct tagged.
func (p *Probe) probeOmitsZero(tagged reflect.Type) omitResult {
	out, err := p.encode(reflect.Zero(tagged))
	if err != nil {
		return omitResult{err: fmt.Errorf("%w: %w", ErrValue, cause(err))}
	}

	_, hasMember := openObject(out)

	return omitResult{omitted: !hasMember}
}

// oneField returns the struct type holding sf alone as its field F, with
// the embedded flag cleared.
func oneField(sf reflect.StructField) reflect.Type {
	return reflect.StructOf([]reflect.StructField{{Name: "F", Type: sf.Type, Tag: sf.Tag}})
}

// firstMemberIsString reports whether the first member of the object in doc
// is a JSON string. An empty object (the field was omitted) reads as false.
func firstMemberIsString(doc []byte) bool {
	dec, hasMember := openObject(doc)
	if !hasMember {
		return false
	}

	_, err := dec.ReadToken()
	if err != nil {
		return false
	}

	return dec.PeekKind() == '"'
}

// openObject reads the opening brace of the object in doc and reports
// whether a member follows it, returning the decoder positioned at that
// member's name.
func openObject(doc []byte) (*jsontext.Decoder, bool) {
	dec := jsontext.NewDecoder(bytes.NewReader(doc))

	tok, err := dec.ReadToken()
	if err != nil || tok.Kind() != '{' {
		return dec, false
	}

	return dec, dec.PeekKind() != '}'
}

// Type reports whether v2 refuses a filled value of t. A fault on t itself is
// [ErrValue]; a fault on the key of a map type is [ErrMapKey]; a fault inside
// an element or member belongs to that type, which the caller probes on its
// own, and reads as no refusal here. The filled value allocates every pointer
// and sizes every container, so a func, chan, complex, or [unsafe.Pointer]
// kind, a [time.Duration], and a key v2 cannot name all surface.
func (p *Probe) Type(t reflect.Type) error {
	if err, ok := p.types[t]; ok {
		return err
	}

	err := p.probeType(t)
	p.types[t] = err

	return err
}

func (p *Probe) probeType(t reflect.Type) error {
	_, err := p.encode(p.filled(t))
	if err == nil {
		return nil
	}

	var serr *json.SemanticError

	if !errors.As(err, &serr) {
		return fmt.Errorf("%w: %w", ErrValue, err)
	}

	if t.Kind() == reflect.Map && serr.GoType == t.Key() {
		return fmt.Errorf("%w: %w", ErrMapKey, cause(err))
	}

	if serr.GoType == t || serr.JSONPointer == "" {
		return fmt.Errorf("%w: %w", ErrValue, cause(err))
	}

	return nil
}

// filled returns a value of t with every pointer allocated and every
// container populated.
func (p *Probe) filled(t reflect.Type) reflect.Value {
	v := reflect.New(t)
	fuzzfill.Fill(v, fillBlob, fillOptions...)

	return v.Elem()
}

// encode marshals v under the probe's options. A panic (a user IsZero under
// omitzero) reports as an error with no semantic detail.
func (p *Probe) encode(v reflect.Value) ([]byte, error) {
	var (
		out []byte
		err error
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				out, err = nil, fmt.Errorf("panic during marshal: %v", r)
			}
		}()

		out, err = json.Marshal(v.Interface(), p.opts)
	}()

	return out, err
}

// cause returns the reason inside a semantic error, or err itself when it is
// not one or v2 recorded no reason.
func cause(err error) error {
	var serr *json.SemanticError

	if errors.As(err, &serr) && serr.Err != nil {
		return serr.Err
	}

	return err
}
