package jsonvalue

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"sync"
)

// pooledDecoder pairs a [jsontext.Decoder] with the [bytes.Reader] it reads
// from, so [Decode] resets both per call instead of allocating them.
type pooledDecoder struct {
	dec *jsontext.Decoder
	rd  bytes.Reader
}

// newPooledDecoder builds a decoder over its own empty reader.
func newPooledDecoder() *pooledDecoder {
	d := &pooledDecoder{}
	d.dec = jsontext.NewDecoder(&d.rd)

	return d
}

// decoders holds idle decoders for Decode, each keeping the read buffer its
// last document grew, so a loop decoding small instances allocates neither a
// decoder nor a buffer per call. Nothing the walk returns aliases that
// buffer: [jsontext.Token.String] copies the bytes of every string and
// number, the container constructors build fresh Values, and the errors
// decodeExact returns carry an offset and one formatted byte, so a later
// call overwriting the buffer cannot reach an earlier result.
var decoders = sync.Pool{
	New: func() any { return newPooledDecoder() },
}

// Decode decodes one JSON document by walking the token stream of a
// [jsontext.Decoder]. Numbers keep their exact literal (so -0, 1e400, and
// integers beyond 2^53 keep their text). The walk accepts one JSON value
// with optional whitespace around it and rejects a document carrying data
// after that value. The decoder's defaults reject duplicate object member
// names, invalid UTF-8, and nesting deeper than 10000 levels, so the walk
// itself needs no such checks.
func Decode(data []byte) (Value, error) {
	d, ok := decoders.Get().(*pooledDecoder)
	if !ok {
		d = newPooledDecoder()
	}

	d.rd.Reset(data)
	d.dec.Reset(&d.rd)

	v, err := decodeExact(d.dec)

	// Clearing the reader drops its reference to data, so an idle decoder
	// pins none of the caller's bytes.
	d.rd.Reset(nil)
	decoders.Put(d)

	if err != nil {
		return Value{}, fmt.Errorf("JSON decode: %w", err)
	}

	return v, nil
}

// DecodeReader is [Decode] over an [io.Reader]. The walk reads r to EOF, so
// it rejects trailing data the same way, along with duplicate object member
// names and invalid UTF-8, and a read error from r surfaces through the
// returned error.
func DecodeReader(r io.Reader) (Value, error) {
	v, err := decodeExact(jsontext.NewDecoder(r))
	if err != nil {
		return Value{}, fmt.Errorf("JSON decode: %w", err)
	}

	return v, nil
}

// decodeExact reads exactly one top-level value from dec and then requires
// EOF. It reports an empty or whitespace-only input as an unexpected EOF at
// the input's end, and a second top-level value with the same wording
// [encoding/json/v2] uses for one, so callers matching on either text see no
// difference from [encoding/json.Unmarshal].
func decodeExact(dec *jsontext.Decoder) (Value, error) {
	v, err := readValue(dec)
	if err != nil {
		if errors.Is(err, io.EOF) {
			offset := dec.InputOffset() + int64(len(dec.UnreadBuffer()))

			return Value{}, &jsontext.SyntacticError{ByteOffset: offset, Err: io.ErrUnexpectedEOF}
		}

		return Value{}, err
	}

	if dec.PeekKind() != 0 {
		unread := dec.UnreadBuffer()
		rest := bytes.TrimLeft(unread, " \t\r\n")

		return Value{}, &jsontext.SyntacticError{
			ByteOffset: dec.InputOffset() + int64(len(unread)-len(rest)),
			Err:        fmt.Errorf("invalid character %q after top-level value", rest[0]),
		}
	}

	// Trailing garbage that starts no JSON value, and a reader error after
	// the value, both surface here rather than through PeekKind.
	_, err = dec.ReadToken()
	if !errors.Is(err, io.EOF) {
		return Value{}, err
	}

	return v, nil
}

// readValue decodes the next complete JSON value from dec, recursing through
// objects and arrays.
func readValue(dec *jsontext.Decoder) (Value, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return Value{}, err
	}

	switch tok.Kind() {
	case 'n':
		return NewNull(), nil
	case 'f':
		return NewBool(false), nil
	case 't':
		return NewBool(true), nil
	case '"':
		return NewString(tok.String()), nil
	case '0':
		return NewNumber(tok.String()), nil
	case '{':
		return readObject(dec)
	case '[':
		return readArray(dec)
	default:
		return Value{}, fmt.Errorf("unexpected token %v", tok)
	}
}

// readObject decodes the members of an object whose opening brace dec has
// already consumed, through its closing brace.
func readObject(dec *jsontext.Decoder) (Value, error) {
	members := map[string]Value{}

	for dec.PeekKind() != '}' {
		nameTok, err := dec.ReadToken()
		if err != nil {
			return Value{}, err
		}

		// The token's bytes are valid only until the next decoder call, and
		// String copies them.
		name := nameTok.String()

		v, err := readValue(dec)
		if err != nil {
			return Value{}, err
		}

		members[name] = v
	}

	_, err := dec.ReadToken()
	if err != nil {
		return Value{}, err
	}

	return NewObject(members), nil
}

// readArray decodes the elements of an array whose opening bracket dec has
// already consumed, through its closing bracket.
func readArray(dec *jsontext.Decoder) (Value, error) {
	elems := []Value{}

	for dec.PeekKind() != ']' {
		v, err := readValue(dec)
		if err != nil {
			return Value{}, err
		}

		elems = append(elems, v)
	}

	_, err := dec.ReadToken()
	if err != nil {
		return Value{}, err
	}

	return NewArray(elems), nil
}
