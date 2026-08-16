package format_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIDNHostnameWrapsUnderlyingError covers the error chain of the
// idn-hostname validator: when a label is rejected by golang.org/x/net/idna or
// by the RFC 5892 contextual rules, the underlying error is wrapped with %w
// rather than flattened into a string, so errors.Unwrap reaches it.
func TestIDNHostnameWrapsUnderlyingError(t *testing.T) {
	t.Parallel()

	validate := validator(t, "idn-hostname")

	tests := map[string]struct {
		instance string
		want     string // message of the wrapped underlying error
	}{
		"idna rejection is wrapped": {
			instance: "≠.example",
			want:     "idna: disallowed rune U+2260",
		},
		"contextual-rule rejection through an A-label is wrapped": {
			// The A-label xn--ab-0ea decodes to "a·b"; the MIDDLE DOT
			// violates the RFC 5892 Appendix A rule requiring 'l' on both
			// sides.
			instance: "xn--ab-0ea.example",
			want:     "invalid hostname: MIDDLE DOT not between two 'l' characters",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			require.Error(t, err,
				"invalid idn-hostname should be rejected by the idn-hostname validator")

			wrapped := errors.Unwrap(err)
			require.Error(t, wrapped,
				"the underlying rejection should stay reachable through the error chain")
			require.Equal(t, tc.want, wrapped.Error())
		})
	}
}
