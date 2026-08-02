package format_test

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Rig 3, layer 3 -- vendored conformance corpus for email. The
// dominicsayers/isemail suite is the reference corpus for the RFC 5321/5322
// address grammars; its 164 cases are vendored under testdata/corpus/isemail/
// with the upstream BSD-3-Clause LICENSE and a README recording provenance.
//
// The corpus grades an address on the RFC 5322 spectrum -- valid, valid but
// deprecated, valid 5322 only, malformed -- while both drafts define the email
// format by grammar conformance and this validator implements the RFC 5321
// Mailbox grammar: no CFWS, no obs- productions, address literals accepted. The
// expectation therefore comes from a category table rather than from the
// per-case diagnosis, and the table is the statement of which grammar this
// format asserts.

// isEmailCategory is one of the corpus's outcome categories.
type isEmailCategory string

var (
	// Each corpus category mapped to whether an address in it is a valid RFC
	// 5321 Mailbox, which is what the email format asserts.
	isEmailExpectations = map[isEmailCategory]bool{
		// Grammar-valid addresses.
		"ISEMAIL_VALID_CATEGORY": true,

		// Valid grammar; the concern is DNS resolution, which a format assertion
		// over a string cannot and must not decide.
		"ISEMAIL_DNSWARN": true,

		// Valid RFC 5321: address literals, quoted strings, a numeric TLD. These
		// are exactly the productions Mailbox has and RFC 5322's addr-spec grades
		// as noteworthy.
		"ISEMAIL_RFC5321": true,

		// Comments and folding whitespace are RFC 5322 constructs with no Mailbox
		// production, so they reject.
		"ISEMAIL_CFWS": false,

		// Obsolete RFC 5322 productions, likewise absent from Mailbox.
		"ISEMAIL_DEPREC": false,

		// Valid RFC 5322 but not a valid RFC 5321 Mailbox.
		"ISEMAIL_RFC5322": false,

		// Malformed under every grammar.
		"ISEMAIL_ERR": false,
	}

	// Per-case expectations pinned where the validator and the corpus disagree
	// for a reason that is not a defect, keyed by corpus case id.
	isEmailOverrides = map[string]struct {
		valid  bool
		reason string
	}{
		// The address literal test@[IPv6:1111:2222:3333:4444:5555:6666::8888],
		// which the corpus grades deprecated because is_email applies RFC
		// 5321's IPv6-comp rule strictly, where "::" must elide more than one
		// 16-bit group. This validator hands the literal to net.ParseIP, which
		// accepts a "::" eliding exactly one group, as RFC 4291 §2.2 permits.
		"71": {
			valid:  true,
			reason: "net.ParseIP accepts a :: eliding one group; RFC 5321 IPv6-comp requires more than one",
		},
	}
)

// isEmailCase is one decoded corpus case.
type isEmailCase struct {
	id       string
	address  string
	category isEmailCategory
}

// TestEmailCorpus runs the vendored is_email corpus through the email format.
func TestEmailCorpus(t *testing.T) {
	t.Parallel()

	runISEmailCorpus(t, "email")
}

// runISEmailCorpus drives every corpus case against the named format, which
// asserts the RFC 5321 Mailbox grammar or a documented widening of it.
func runISEmailCorpus(t *testing.T, name string) {
	t.Helper()

	validate := validator(t, name)

	cases := loadISEmailCorpus(t)
	require.Len(t, cases, 164, "corpus case count changed; refresh the counts in the corpus README")

	ids := make(map[string]bool, len(cases))

	for _, tc := range cases {
		ids[tc.id] = true

		// Exhaustiveness: an unmapped category would otherwise default to a
		// silent reject and the new cases would pass for the wrong reason.
		_, known := isEmailExpectations[tc.category]
		require.True(t, known, "corpus case %s has unmapped category %q", tc.id, tc.category)
	}

	// Liveness: an override naming a case that no longer exists would sit
	// unexercised and mask a regression after a refresh.
	for id := range isEmailOverrides {
		require.True(t, ids[id], "override for case %s is no longer in the corpus; drop it", id)
	}

	for _, tc := range cases {
		t.Run(tc.id+"/"+string(tc.category), func(t *testing.T) {
			t.Parallel()

			want := isEmailExpectations[tc.category]
			why := "category " + string(tc.category)

			if override, ok := isEmailOverrides[tc.id]; ok {
				want, why = override.valid, override.reason
			}

			err := validate(tc.address)
			if want {
				require.NoError(t, err, "%s should be a valid %s (%s)", tc.address, name, why)
			} else {
				require.Error(t, err, "%s should be an invalid %s (%s)", tc.address, name, why)
			}
		})
	}
}

// loadISEmailCorpus decodes the vendored tests.xml. The id is an attribute, not
// a child element; decoding it as a child yields "" for every case and silently
// defeats the per-case overrides. Case 1 carries a self-closing <address/>,
// which decodes to the empty string and is the test, so it is run rather than
// skipped.
func loadISEmailCorpus(t *testing.T) []isEmailCase {
	t.Helper()

	path := filepath.Join("testdata", "corpus", "isemail", "tests.xml")

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read corpus file %s", path)

	var doc struct {
		Tests []struct {
			ID       string          `xml:"id,attr"`
			Address  string          `xml:"address"`
			Category isEmailCategory `xml:"category"`
		} `xml:"test"`
	}

	require.NoError(t, xml.Unmarshal(data, &doc), "decode corpus file %s", path)

	cases := make([]isEmailCase, 0, len(doc.Tests))
	for _, test := range doc.Tests {
		cases = append(cases, isEmailCase{
			id:       test.ID,
			address:  decodeControlSymbols(test.Address),
			category: test.Category,
		})
	}

	return cases
}

// decodeControlSymbols restores the control characters the corpus stores as
// U+2400 + n, the "SYMBOL FOR" block, because XML cannot carry them. U+2400
// through U+2420 map to NUL through SPACE by subtraction; U+2421 is SYMBOL FOR
// DELETE and maps to U+007F, which the same subtraction would turn into '!'.
func decodeControlSymbols(s string) string {
	if !strings.ContainsFunc(s, isControlSymbol) {
		return s
	}

	return strings.Map(func(r rune) rune {
		switch {
		case r == '␡':
			return 0x7F
		case r >= '␀' && r <= '␠':
			return r - 0x2400
		}

		return r
	}, s)
}

// isControlSymbol reports whether r is in the "SYMBOL FOR" range the corpus uses
// to stand in for a control character.
func isControlSymbol(r rune) bool {
	return r >= '␀' && r <= '␡'
}
