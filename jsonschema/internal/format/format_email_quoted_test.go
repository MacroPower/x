package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmailQuotedLocalCharacterRules covers the RFC 5321 quoted-string
// grammar for the local part: qtextSMTP is printable ASCII (%d32-33 /
// %d35-91 / %d93-126) and quoted-pairSMTP may escape only %d32-126. Raw
// non-ASCII text in a quoted local part is the RFC 6531 idn-email widening
// and must not leak into the plain email format, and an escaped control
// character is invalid for both formats (RFC 6531 extends qtextSMTP only,
// not quoted-pairSMTP).
func TestEmailQuotedLocalCharacterRules(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format   string
		instance string
		want     bool
	}{
		"email rejects non-ASCII qtext": {
			format:   "email",
			instance: "\"日本\"@example.com",
			want:     false,
		},
		"email rejects escaped control character": {
			format:   "email",
			instance: "\"a\\\x01b\"@example.com",
			want:     false,
		},
		"email rejects escaped DEL": {
			format:   "email",
			instance: "\"a\\\x7fb\"@example.com",
			want:     false,
		},
		"email rejects escaped non-ASCII": {
			format:   "email",
			instance: "\"a\\é\"@example.com",
			want:     false,
		},
		"email accepts printable quoted text": {
			format:   "email",
			instance: `"ok"@example.com`,
			want:     true,
		},
		"email accepts escaped quote and backslash": {
			format:   "email",
			instance: `"a\"b\\c"@example.com`,
			want:     true,
		},
		"email accepts quoted space": {
			format:   "email",
			instance: `"a b"@example.com`,
			want:     true,
		},
		"idn-email rejects escaped control character": {
			format:   "idn-email",
			instance: "\"a\\\x01b\"@example.com",
			want:     false,
		},
		"idn-email rejects escaped non-ASCII": {
			format:   "idn-email",
			instance: "\"a\\é\"@example.com",
			want:     false,
		},
		"idn-email rejects malformed UTF-8 in quoted text": {
			format:   "idn-email",
			instance: "\"a\xffb\"@example.com",
			want:     false,
		},
		"idn-email accepts non-ASCII qtext": {
			format:   "idn-email",
			instance: "\"日本\"@example.com",
			want:     true,
		},
		"idn-email accepts escaped printable ASCII": {
			format:   "idn-email",
			instance: `"a\"b"@example.com`,
			want:     true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validator(t, tc.format)(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"valid quoted local part should pass the %s validator", tc.format)
			} else {
				require.Error(t, err,
					"invalid quoted local part should be rejected by the %s validator", tc.format)
			}
		})
	}
}
