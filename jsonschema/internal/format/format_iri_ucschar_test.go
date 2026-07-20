package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIRIUcscharBounds covers the RFC 3987 upper bounds of the non-ASCII
// character rule: iunreserved admits only ucschar (%xA0-D7FF / %xF900-FDCF /
// %xFDF0-FFEF / the supplementary planes minus their trailing noncharacters,
// with the %xE0000-E0FFF gap excluded), and the private-use iprivate set
// (%xE000-F8FF / %xF0000-FFFFD / %x100000-10FFFD) is legal only in the query
// component. The C1-control lower bound is covered by TestIRIRejectsC1Controls.
func TestIRIUcscharBounds(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format   string
		instance string
		want     bool
	}{
		"iri rejects private-use in path": {
			format:   "iri",
			instance: "http://example.com/",
			want:     false,
		},
		"iri rejects replacement character in path": {
			format:   "iri",
			instance: "http://example.com/�",
			want:     false,
		},
		"iri rejects noncharacter U+FFFF in path": {
			format:   "iri",
			instance: "http://example.com/￿",
			want:     false,
		},
		"iri rejects noncharacter U+FDD0 in path": {
			format:   "iri",
			instance: "http://example.com/﷐",
			want:     false,
		},
		"iri rejects plane-end noncharacter in path": {
			format:   "iri",
			instance: "http://example.com/\U0001fffe",
			want:     false,
		},
		"iri rejects tags-plane gap in path": {
			format:   "iri",
			instance: "http://example.com/\U000e0001",
			want:     false,
		},
		"iri rejects private-use in fragment": {
			format:   "iri",
			instance: "http://example.com/p#",
			want:     false,
		},
		"iri rejects supplementary private-use in path": {
			format:   "iri",
			instance: "http://example.com/\U000f0000",
			want:     false,
		},
		"iri accepts private-use in query": {
			format:   "iri",
			instance: "http://example.com/p?v=",
			want:     true,
		},
		"iri accepts supplementary private-use in query": {
			format:   "iri",
			instance: "http://example.com/p?v=\U000f0000",
			want:     true,
		},
		"iri accepts ucschar in path": {
			format:   "iri",
			instance: "http://example.com/café",
			want:     true,
		},
		"iri accepts supplementary ucschar in path": {
			format:   "iri",
			instance: "http://example.com/\U0001f600",
			want:     true,
		},
		"iri-reference rejects private-use in path": {
			format:   "iri-reference",
			instance: "/",
			want:     false,
		},
		"iri-reference rejects noncharacter": {
			format:   "iri-reference",
			instance: "/￿",
			want:     false,
		},
		"iri-reference accepts private-use in query": {
			format:   "iri-reference",
			instance: "/p?v=",
			want:     true,
		},
		"iri-reference rejects private-use after fragment question mark": {
			format:   "iri-reference",
			instance: "/p#frag?",
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
					"code point outside the RFC 3987 sets should be rejected by the %s validator", tc.format)
			}
		})
	}
}
