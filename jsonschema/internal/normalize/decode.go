package normalize

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"

	jsonv1 "encoding/json"
)

// UnmarshalExact makes any-typed decode targets receive JSON numbers as
// exact [jsonv1.Number] literals instead of float64, preserving the
// integer vs number distinction that the validator relies on. It is the
// [encoding/json/v2] replacement for v1's Decoder.UseNumber and applies
// recursively through nested objects and arrays.
var UnmarshalExact = json.WithUnmarshalers(json.UnmarshalFromFunc(
	func(dec *jsontext.Decoder, val *any) error {
		if dec.PeekKind() != '0' {
			// Not a number: decline, so the default decoding applies.
			return errors.ErrUnsupported
		}

		tok, err := dec.ReadToken()
		if err != nil {
			return err //nolint:wrapcheck // The unmarshal frame adds the context.
		}

		*val = jsonv1.Number(tok.String())

		return nil
	},
))

// DecodeJSONInstance decodes JSON bytes into an instance value with
// [UnmarshalExact], so numbers survive as exact [jsonv1.Number] literals. It
// rejects a document carrying data after the single top-level value
// ([json.Unmarshal] accepts one JSON value with optional whitespace around
// it), and, per [encoding/json/v2] defaults, one carrying duplicate object
// member names or invalid UTF-8.
func DecodeJSONInstance(data []byte) (any, error) {
	var instance any

	err := json.Unmarshal(data, &instance, UnmarshalExact)
	if err != nil {
		return nil, fmt.Errorf("JSON decode: %w", err)
	}

	return instance, nil
}

// DecodeJSONInstanceReader is [DecodeJSONInstance] over an [io.Reader].
// [json.UnmarshalRead] reads r to EOF, so it rejects trailing data the same
// way, along with duplicate object member names and invalid UTF-8.
func DecodeJSONInstanceReader(r io.Reader) (any, error) {
	var instance any

	err := json.UnmarshalRead(r, &instance, UnmarshalExact)
	if err != nil {
		return nil, fmt.Errorf("JSON decode: %w", err)
	}

	return instance, nil
}
