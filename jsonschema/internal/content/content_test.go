package content_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/content"
)

func TestMediaTypeIsJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mediaType string
		want      bool
	}{
		"plain":                   {mediaType: "application/json", want: true},
		"uppercase":               {mediaType: "Application/JSON", want: true},
		"with charset parameter":  {mediaType: "application/json; charset=utf-8", want: true},
		"untrimmed with param":    {mediaType: "application/json ; charset=utf-8", want: true},
		"other type":              {mediaType: "text/plain", want: false},
		"empty":                   {mediaType: "", want: false},
		"json suffix not subtype": {mediaType: "application/ld+json", want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, content.MediaTypeIsJSON(tc.mediaType))
		})
	}
}

func TestAssert(t *testing.T) {
	t.Parallel()

	validB64 := base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))
	badJSONB64 := base64.StdEncoding.EncodeToString([]byte(`{not json`))

	tests := map[string]struct {
		encoding  string
		mediaType string
		str       string
		keyword   string
		decodeErr bool
	}{
		"no keywords passes": {
			str: "anything",
		},
		"valid base64 only": {
			encoding: content.Base64,
			str:      validB64,
		},
		"invalid base64": {
			encoding:  content.Base64,
			str:       "not!base64!",
			keyword:   "contentEncoding",
			decodeErr: true,
		},
		"json media type valid": {
			mediaType: "application/json",
			str:       `{"a":1}`,
		},
		"json media type invalid": {
			mediaType: "application/json",
			str:       `{not json`,
			keyword:   "contentMediaType",
		},
		"base64 then valid json": {
			encoding:  content.Base64,
			mediaType: "application/json",
			str:       validB64,
		},
		"base64 then invalid json": {
			encoding:  content.Base64,
			mediaType: "application/json",
			str:       badJSONB64,
			keyword:   "contentMediaType",
		},
		"unknown encoding does not assert media type": {
			encoding:  "quoted-printable",
			mediaType: "application/json",
			str:       `{not json`, // not asserted: encoding unknown, so undecodable
		},
		"non-json media type ignored": {
			mediaType: "text/plain",
			str:       `{not json`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			kw, decErr := content.Assert(tc.encoding, tc.mediaType, tc.str)
			assert.Equal(t, tc.keyword, kw)

			if tc.decodeErr {
				require.Error(t, decErr)
			} else {
				assert.NoError(t, decErr)
			}
		})
	}
}
