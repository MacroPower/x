package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmailRejectsTrailingDotDomain covers the RFC 5321 Domain grammar
// (sub-domain *("." sub-domain)), which has no trailing-dot production: the
// DNS root-dot convention belongs to the hostname and idn-hostname formats
// only, so "user@example.com." is not a valid mailbox for either email
// format. The idn-email path must also reject the IDNA dot variants
// (e.g. U+3002) as trailing separators.
func TestEmailRejectsTrailingDotDomain(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format   string
		instance string
		want     bool
	}{
		"email rejects trailing-dot domain": {
			format:   "email",
			instance: "user@example.com.",
			want:     false,
		},
		"email accepts the same domain without the dot": {
			format:   "email",
			instance: "user@example.com",
			want:     true,
		},
		"idn-email rejects trailing-dot domain": {
			format:   "idn-email",
			instance: "user@example.com.",
			want:     false,
		},
		"idn-email rejects trailing ideographic full stop": {
			format:   "idn-email",
			instance: "user@example.com。",
			want:     false,
		},
		"idn-email accepts the same domain without the dot": {
			format:   "idn-email",
			instance: "user@example.com",
			want:     true,
		},
		// The hostname formats keep the FQDN root-dot allowance.
		"hostname keeps trailing-dot FQDN": {
			format:   "hostname",
			instance: "example.com.",
			want:     true,
		},
		"idn-hostname keeps trailing-dot FQDN": {
			format:   "idn-hostname",
			instance: "example.com.",
			want:     true,
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
					"trailing-dot domain should be rejected by the %s validator", tc.format)
			}
		})
	}
}
