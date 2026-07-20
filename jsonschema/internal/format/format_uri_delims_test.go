package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIDelimPlacement covers the two RFC 3986 gen-delim rules net/url
// tolerates: '#' may appear only once, as the fragment delimiter (fragment =
// query = *( pchar / "/" / "?" ) contains no '#'), and '[' / ']' are
// permitted only as the brackets of an authority IP-literal host. The same
// rules reach iri and iri-reference through the shared helpers (RFC 3987
// inherits them from RFC 3986).
func TestURIDelimPlacement(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format   string
		instance string
		want     bool
	}{
		"uri rejects second hash in fragment": {
			format:   "uri",
			instance: "http://x/#a#b",
			want:     false,
		},
		"uri rejects brackets in path": {
			format:   "uri",
			instance: "http://example.com/a[b]",
			want:     false,
		},
		"uri rejects bracket in query": {
			format:   "uri",
			instance: "http://example.com/?a[1]=2",
			want:     false,
		},
		"uri accepts bracketed IPv6 host": {
			format:   "uri",
			instance: "ldap://[2001:db8::7]/c=GB?objectClass?one",
			want:     true,
		},
		"uri accepts single fragment": {
			format:   "uri",
			instance: "http://foo.bar/?baz=qux#quux",
			want:     true,
		},
		"uri accepts percent-encoded brackets": {
			format:   "uri",
			instance: "http://example.com/a%5Bb%5D",
			want:     true,
		},
		"uri-reference rejects second hash": {
			format:   "uri-reference",
			instance: "#a#b",
			want:     false,
		},
		"uri-reference rejects brackets in path": {
			format:   "uri-reference",
			instance: "a[b]",
			want:     false,
		},
		"uri-reference accepts single fragment": {
			format:   "uri-reference",
			instance: "#fragment",
			want:     true,
		},
		"iri rejects second hash in fragment": {
			format:   "iri",
			instance: "http://x/#a#b",
			want:     false,
		},
		"iri rejects brackets in path": {
			format:   "iri",
			instance: "http://example.com/a[b]",
			want:     false,
		},
		"iri accepts bracketed IPv6 host": {
			format:   "iri",
			instance: "http://[2001:0db8:85a3:0000:0000:8a2e:0370:7334]",
			want:     true,
		},
		"iri-reference rejects brackets in path": {
			format:   "iri-reference",
			instance: "a[b]",
			want:     false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validator(t, tc.format)(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"instance should pass the %s validator", tc.format)
			} else {
				require.Error(t, err,
					"misplaced gen-delim should be rejected by the %s validator", tc.format)
			}
		})
	}
}
