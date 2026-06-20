package normalize

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// errTrailingData reports tokens after the single top-level JSON value.
var errTrailingData = errors.New("unexpected data after top-level value")

// DecodeJSONInstance decodes JSON bytes into an instance value using
// [json.Decoder] with UseNumber(), preserving the integer vs number distinction
// that the validator relies on. It rejects a document carrying tokens after the
// single top-level value.
func DecodeJSONInstance(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var instance any

	err := dec.Decode(&instance)
	if err != nil {
		return nil, fmt.Errorf("JSON decode: %w", err)
	}

	// A JSON document is a single value. The decoder stops after the first
	// value and leaves any remaining tokens in the stream, so an exhausted
	// stream is required to reject documents like `{"a":1} x` or `true false`.
	// Token skips insignificant whitespace, so trailing whitespace still
	// reaches io.EOF and is accepted.
	_, err = dec.Token()
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("JSON decode: %w", errTrailingData)
		}

		return nil, fmt.Errorf("JSON decode: %w", err)
	}

	return instance, nil
}
