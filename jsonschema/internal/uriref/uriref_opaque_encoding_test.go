package uriref_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// TestResolveURIOpaqueEncodedRef asserts that the opaque/URN merge operates on
// the encoded form of a relative ref, the same way the hierarchical branch
// resolves on escaped paths. A percent-escape in the ref survives into the
// resolved URI: decoding it would register a schema under a different key than
// the absolute URN spelling a $ref recomputes, and a decoded space would
// produce a syntactically invalid URI. An encoded fragment likewise keeps its
// raw spelling, so a JSON Pointer with an escaped separator splits correctly.
func TestResolveURIOpaqueEncodedRef(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base string
		ref  string
		want string
	}{
		"escaped slash stays encoded": {
			base: "urn:example:root",
			ref:  "a%2Fb",
			want: "urn:example:a%2Fb",
		},
		"escaped space stays encoded": {
			base: "urn:example:root",
			ref:  "su%20b",
			want: "urn:example:su%20b",
		},
		"encoded fragment survives the merge": {
			base: "urn:example:root",
			ref:  "sub#/a%2Fb",
			want: "urn:example:sub#/a%2Fb",
		},
		"unencoded ref unchanged": {
			base: "urn:example:root",
			ref:  "sub",
			want: "urn:example:sub",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := uriref.ResolveURI(tc.base, tc.ref)
			assert.Equal(t, tc.want, got)

			// The resolved key survives a url.Parse round-trip byte for byte,
			// the symmetry the registry relies on: the absolute spelling of the
			// same target parses and re-serializes to this exact key.
			parsed, err := url.Parse(tc.want)
			require.NoError(t, err)
			assert.Equal(t, got, parsed.String())
		})
	}
}
