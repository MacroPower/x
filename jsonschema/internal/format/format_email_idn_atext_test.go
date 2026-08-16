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

// TestIDNEmailDotAtomNonASCIIWidening covers the breadth of the RFC 6531 atext
// widening in the unquoted local part: UTF8-non-ascii (RFC 6532 §3.1) admits
// every non-ASCII code point with no whitespace or control carve-out, so
// NO-BREAK SPACE and C1 controls are valid there -- matching the quoted-local
// scan, which applies the same widening -- while the ASCII atext exclusions
// (space, tab, specials) still hold.
func TestIDNEmailDotAtomNonASCIIWidening(t *testing.T) {
	t.Parallel()

	validate := validator(t, "idn-email")

	tests := map[string]struct {
		instance string
		want     bool
	}{
		"NO-BREAK SPACE in unquoted local part is valid": {
			instance: "a\u00a0b@example.com",
			want:     true,
		},
		"NEXT LINE (C1 control) in unquoted local part is valid": {
			instance: "a\u0085b@example.com",
			want:     true,
		},
		"C1 control U+009F in unquoted local part is valid": {
			instance: "a\u009fb@example.com",
			want:     true,
		},
		"IDEOGRAPHIC SPACE in unquoted local part is valid": {
			instance: "a\u3000b@example.com",
			want:     true,
		},
		"quoted spelling of NO-BREAK SPACE is valid": {
			instance: "\"a\u00a0b\"@example.com",
			want:     true,
		},
		"ASCII space in unquoted local part is invalid": {
			instance: "a b@example.com",
			want:     false,
		},
		"ASCII tab in unquoted local part is invalid": {
			instance: "a\tb@example.com",
			want:     false,
		},
		"ASCII comma in unquoted local part is invalid": {
			instance: "a,b@example.com",
			want:     false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"RFC 6531-valid local part should pass the idn-email validator")
			} else {
				require.Error(t, err,
					"RFC 6531-invalid local part should be rejected by the idn-email validator")
			}
		})
	}
}
