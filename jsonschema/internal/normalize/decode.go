package normalize

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"

	jsonv1 "encoding/json"
)

// DecodeJSONInstance decodes JSON bytes into an instance value by walking the
// token stream of a [jsontext.Decoder]. Numbers survive as exact
// [jsonv1.Number] literals (so -0, 1e400, and integers beyond 2^53 keep their
// text), objects decode to map[string]any, and arrays to []any. The walk
// accepts one JSON value with optional whitespace around it and rejects a
// document carrying data after that value. The decoder's defaults reject
// duplicate object member names, invalid UTF-8, and nesting deeper than
// 10000 levels, so the walk itself needs no such checks.
func DecodeJSONInstance(data []byte) (any, error) {
	instance, err := decodeExact(jsontext.NewDecoder(bytes.NewReader(data)))
	if err != nil {
		return nil, fmt.Errorf("JSON decode: %w", err)
	}

	return instance, nil
}

// DecodeJSONInstanceReader is [DecodeJSONInstance] over an [io.Reader]. The
// walk reads r to EOF, so it rejects trailing data the same way, along with
// duplicate object member names and invalid UTF-8, and a read error from r
// surfaces through the returned error.
func DecodeJSONInstanceReader(r io.Reader) (any, error) {
	instance, err := decodeExact(jsontext.NewDecoder(r))
	if err != nil {
		return nil, fmt.Errorf("JSON decode: %w", err)
	}

	return instance, nil
}

// decodeExact reads exactly one top-level value from dec and then requires
// EOF. It reports an empty or whitespace-only input as an unexpected EOF at
// the input's end, and a second top-level value with the same wording
// [encoding/json/v2] uses for one, so callers matching on either text see no
// difference from [json.Unmarshal].
func decodeExact(dec *jsontext.Decoder) (any, error) {
	value, err := readValue(dec)
	if err != nil {
		if errors.Is(err, io.EOF) {
			offset := dec.InputOffset() + int64(len(dec.UnreadBuffer()))

			return nil, &jsontext.SyntacticError{ByteOffset: offset, Err: io.ErrUnexpectedEOF}
		}

		return nil, err
	}

	if dec.PeekKind() != 0 {
		unread := dec.UnreadBuffer()
		rest := bytes.TrimLeft(unread, " \t\r\n")

		return nil, &jsontext.SyntacticError{
			ByteOffset: dec.InputOffset() + int64(len(unread)-len(rest)),
			Err:        fmt.Errorf("invalid character %q after top-level value", rest[0]),
		}
	}

	// Trailing garbage that starts no JSON value, and a reader error after
	// the value, both surface here rather than through PeekKind.
	_, err = dec.ReadToken()
	if !errors.Is(err, io.EOF) {
		return nil, err
	}

	return value, nil
}

// readValue decodes the next complete JSON value from dec, recursing through
// objects and arrays.
func readValue(dec *jsontext.Decoder) (any, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}

	switch tok.Kind() {
	case 'n':
		return nil, nil //nolint:nilnil // A JSON null decodes to a nil any.
	case 'f':
		return false, nil
	case 't':
		return true, nil
	case '"':
		return tok.String(), nil
	case '0':
		return jsonv1.Number(tok.String()), nil
	case '{':
		return readObject(dec)
	case '[':
		return readArray(dec)
	default:
		return nil, fmt.Errorf("unexpected token %v", tok)
	}
}

// readObject decodes the members of an object whose opening brace dec has
// already consumed, through its closing brace.
func readObject(dec *jsontext.Decoder) (map[string]any, error) {
	members := map[string]any{}

	for dec.PeekKind() != '}' {
		nameTok, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}

		// The token's bytes are valid only until the next decoder call, and
		// String copies them.
		name := nameTok.String()

		value, err := readValue(dec)
		if err != nil {
			return nil, err
		}

		members[name] = value
	}

	_, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}

	return members, nil
}

// readArray decodes the elements of an array whose opening bracket dec has
// already consumed, through its closing bracket.
func readArray(dec *jsontext.Decoder) ([]any, error) {
	elements := []any{}

	for dec.PeekKind() != ']' {
		value, err := readValue(dec)
		if err != nil {
			return nil, err
		}

		elements = append(elements, value)
	}

	_, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}

	return elements, nil
}
