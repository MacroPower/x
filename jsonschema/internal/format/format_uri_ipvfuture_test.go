package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIAcceptsIPvFutureHost covers the RFC 3986 IPvFuture host literal
// (IP-literal = "[" ( IPv6address / IPvFuture ) "]", IPvFuture = "v" 1*HEXDIG
// "." 1*( unreserved / sub-delims / ":" )), which net/url rejects because it
// validates bracketed hosts strictly as IP addresses. The validators detect
// the literal and re-parse with a placeholder host, so uri, uri-reference,
// iri, and iri-reference all accept it; malformed vN.x literals stay invalid.
func TestURIAcceptsIPvFutureHost(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format   string
		instance string
		want     bool
	}{
		"uri accepts IPvFuture host": {
			format:   "uri",
			instance: "http://[v1.fe80::a+en1]/",
			want:     true,
		},
		"uri accepts uppercase V": {
			format:   "uri",
			instance: "http://[V1.a]/",
			want:     true,
		},
		"uri accepts userinfo and port around IPvFuture": {
			format:   "uri",
			instance: "http://user@[v1.a]:8080/p",
			want:     true,
		},
		"uri-reference accepts IPvFuture authority": {
			format:   "uri-reference",
			instance: "//[v1.a]/",
			want:     true,
		},
		"iri accepts IPvFuture host": {
			format:   "iri",
			instance: "http://[v1.fe80::a+en1]/",
			want:     true,
		},
		"iri-reference accepts IPvFuture authority": {
			format:   "iri-reference",
			instance: "//[v1.a]/",
			want:     true,
		},
		"uri rejects IPvFuture without hex digits": {
			format:   "uri",
			instance: "http://[v.a]/",
			want:     false,
		},
		"uri rejects IPvFuture without dot": {
			format:   "uri",
			instance: "http://[v1a]/",
			want:     false,
		},
		"uri rejects IPvFuture with empty tail": {
			format:   "uri",
			instance: "http://[v1.]/",
			want:     false,
		},
		"uri rejects IPvFuture with forbidden tail character": {
			format:   "uri",
			instance: "http://[v1.a/b]/",
			want:     false,
		},
		"uri still accepts bracketed IPv6 host": {
			format:   "uri",
			instance: "http://[fe80::a]/",
			want:     true,
		},
		"uri still rejects unmatched bracket host": {
			format:   "uri",
			instance: "https://[@example.org/test.txt",
			want:     false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validator(t, tc.format)(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"RFC 3986 IPvFuture host should pass the %s validator", tc.format)
			} else {
				require.Error(t, err,
					"malformed host literal should be rejected by the %s validator", tc.format)
			}
		})
	}
}
