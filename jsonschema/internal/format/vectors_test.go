package format_test

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Rig 3, layer 3 -- curated / vendored acceptance vectors. For formats with an
// authoritative corpus or crisp RFC examples but no clean stdlib oracle, these
// table-driven tests lock the accept/reject boundary against fixed vectors drawn
// from the RFCs' own example text. They complement the robustness fuzz (layer 1)
// and the stdlib differentials (layer 2), and sit alongside the official JSON
// Schema Test Suite's optional/format cases, which already run via
// TestSuiteFormat.

// formatVector is one acceptance vector: an instance and whether the format
// validator must accept it.
type formatVector struct {
	instance string
	valid    bool
}

// runFormatVectors drives a validity table against the named validator.
func runFormatVectors(t *testing.T, name string, cases map[string]formatVector) {
	t.Helper()

	validate := validator(t, name)

	for caseName, tc := range cases {
		t.Run(caseName, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.valid {
				require.NoError(t, err, "%q should be a valid %s", tc.instance, name)
			} else {
				require.Error(t, err, "%q should be an invalid %s", tc.instance, name)
			}
		})
	}
}

// TestURIVectors checks the uri format against RFC 3986 examples. A uri is an
// absolute URI, so it must carry a scheme (RFC 3986 §3): the scheme-less
// reference forms are the rejected cases.
func TestURIVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "uri", map[string]formatVector{
		"http with query and fragment": {"https://example.com/a?b=c#d", true},
		"urn isbn (RFC 3986 §1.1.2)":   {"urn:isbn:0451450523", true},
		"mailto scheme":                {"mailto:John.Doe@example.com", true},
		"ftp scheme (RFC 3986 §1.1.2)": {"ftp://ftp.is.co.za/rfc/rfc1808.txt", true},
		"network-path reference":       {"//example.com", false},
		"absolute-path reference":      {"/relative/path", false},
		"scheme-less host":             {"example.com", false},
		"space is forbidden":           {"http://exa mple.com", false},
	})
}

// TestURIReferenceVectors checks the uri-reference format against RFC 3986 §4.1:
// a reference may be relative, so scheme-less forms are accepted; only forbidden
// characters reject.
func TestURIReferenceVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "uri-reference", map[string]formatVector{
		"absolute uri":            {"https://example.com", true},
		"absolute-path reference": {"/path/only", true},
		"relative-path reference": {"../sibling", true},
		"fragment-only reference": {"#section", true},
		"query-only reference":    {"?q=1", true},
		"empty reference":         {"", true},
		"space is forbidden":      {"http://exa mple", false},
	})
}

// TestEmailVectors checks the email format against RFC 5321 mailbox examples.
func TestEmailVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "email", map[string]formatVector{
		"simple mailbox":      {"user@example.com", true},
		"dotted local part":   {"john.doe@example.org", true},
		"short domain":        {"a@b.co", true},
		"single-label domain": {"a@b", true},
		"no at sign":          {"not-an-email", false},
		"empty local part":    {"@example.com", false},
		"empty domain":        {"user@", false},
		"double at sign":      {"user@@example.com", false},
	})
}

// TestIDNEmailVectors checks the idn-email format (RFC 6531) accepts non-ASCII
// local and domain parts that the ASCII email format would reject.
func TestIDNEmailVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "idn-email", map[string]formatVector{
		"ascii mailbox":    {"user@example.com", true},
		"non-ascii local":  {"宏@example.com", true},
		"non-ascii domain": {"user@例.jp", true},
		"missing at sign":  {"not-an-email", false},
	})
}

// TestUUIDVectors checks the uuid format against RFC 4122. The canonical form is
// 8-4-4-4-12 hex digits; case is insignificant.
func TestUUIDVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "uuid", map[string]formatVector{
		"RFC 4122 §3 example": {"f47ac10b-58cc-4372-a567-0e02b2c3d479", true},
		"nil uuid":            {"00000000-0000-0000-0000-000000000000", true},
		"uppercase accepted":  {"F47AC10B-58CC-4372-A567-0E02B2C3D479", true},
		"too short":           {"f47ac10b-58cc", false},
		"missing hyphens":     {"f47ac10b58cc4372a5670e02b2c3d479", false},
		"non-hex digit":       {"g47ac10b-58cc-4372-a567-0e02b2c3d479", false},
	})
}

// TestDurationVectors checks the duration format against RFC 3339 Appendix A
// (the ISO 8601 duration ABNF). Components must appear in canonical order with
// no gaps, and weeks stand alone.
func TestDurationVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "duration", map[string]formatVector{
		"full date and time":       {"P1Y2M3DT4H5M6S", true},
		"years only":               {"P1Y", true},
		"time only":                {"PT1H", true},
		"weeks alone":              {"P1W", true},
		"zero days":                {"P0D", true},
		"missing P":                {"1Y", false},
		"no components":            {"P", false},
		"hours without T":          {"P1H", false},
		"T without time component": {"PT", false},
		"gap in date chain":        {"P1Y2D", false},
		"weeks mixed with days":    {"P1W2D", false},
	})
}

// TestRegexVectors checks the regex format against ECMA-262 constructs. The
// validator is a structural check (balanced parens, terminated classes, defined
// escapes), so it accepts ECMA-262 patterns RE2 rejects (backreferences,
// lookaround) and rejects only structurally malformed patterns.
func TestRegexVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "regex", map[string]formatVector{
		"anchored class":         {"^[a-z]+$", true},
		"backreference":          {`(foo)\1`, true},
		"lookahead":              {"foo(?=bar)", true},
		"character class":        {"[abc]", true},
		"quantifier braces":      {"a{2,3}", true},
		"unbalanced parenthesis": {"(", false},
		"unterminated class":     {"[", false},
		"empty class":            {"[]", false},
		"trailing backslash":     {`\`, false},
	})
}

// TestHostnameVectors checks the hostname format against RFC 1123 §2.1: labels
// are ASCII letters, digits, and interior hyphens, and the top-level label must
// not be all-numeric.
func TestHostnameVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "hostname", map[string]formatVector{
		"multi-label":          {"example.com", true},
		"single label":         {"a", true},
		"interior hyphen":      {"foo-bar.example", true},
		"ace prefix label":     {"xn--fsq.jp", true},
		"trailing dot on fqdn": {"example.com.", true},
		"leading hyphen":       {"-bad.com", false},
		"trailing hyphen":      {"bad-.com", false},
		"numeric top label":    {"123", false},
		"bare trailing dot":    {"example.", false},
		"empty interior label": {"a..b", false},
		"degenerate ace label": {"xn--", false},
	})
}

// TestIDNHostnameVectors checks the idn-hostname format against the vendored
// vector file, which pins the RFC 5890/5891 label structure the validator
// enforces over golang.org/x/net/idna.
func TestIDNHostnameVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "idn-hostname", loadVectorFile(t, "testdata/idn_hostname_vectors.tsv"))
}

// loadVectorFile reads a tab-separated <input>\t<valid> acceptance-vector file,
// skipping blank and '#' comment lines. The case name is the input itself.
func loadVectorFile(t *testing.T, path string) map[string]formatVector {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read vector file %s", path)

	cases := make(map[string]formatVector)

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		input, validText, ok := strings.Cut(line, "\t")
		require.True(t, ok, "vector line missing tab separator: %q", line)

		cases[input] = formatVector{instance: input, valid: validText == "true"}
	}

	require.NoError(t, scanner.Err(), "scan vector file %s", path)

	return cases
}
