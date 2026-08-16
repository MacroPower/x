package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIDNEmailDotAtomUTF8WellFormed covers the RFC 6531 widening of atext with
// UTF8-non-ascii (RFC 6532 §3.1) in the unquoted local part: only well-formed
// multi-byte sequences qualify, so an ill-formed byte is rejected rather than
// silently decoded to U+FFFD, matching the quoted-local scan and the plain
// email format. A genuine U+FFFD is a well-formed UTF8-3 sequence and stays
// valid.
func TestIDNEmailDotAtomUTF8WellFormed(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format   string
		instance string
		want     bool
	}{
		"idn-email rejects a lone invalid byte in the local part": {
			format:   "idn-email",
			instance: "a\xffb@example.com",
			want:     false,
		},
		"idn-email rejects a truncated multi-byte sequence in the local part": {
			format:   "idn-email",
			instance: "a\xe4\xb8@example.com",
			want:     false,
		},
		"idn-email rejects an overlong encoding in the local part": {
			format:   "idn-email",
			instance: "a\xc0\xafb@example.com",
			want:     false,
		},
		"idn-email accepts a genuine replacement character in the local part": {
			format:   "idn-email",
			instance: "a�b@example.com",
			want:     true,
		},
		"email rejects the same invalid byte in the local part": {
			format:   "email",
			instance: "a\xffb@example.com",
			want:     false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validator(t, tc.format)(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"well-formed local part should pass the %s validator", tc.format)
			} else {
				require.Error(t, err,
					"ill-formed local part should be rejected by the %s validator", tc.format)
			}
		})
	}
}
