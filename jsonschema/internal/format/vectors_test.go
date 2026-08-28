package format_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
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
// TestSuiteFormat. Each vector file names the suite file it complements and
// carries only what that file leaves open, so RFC 3339's leap second and the RFC
// 6901 §5 pointer examples are not re-transcribed. Those §5 examples are all
// valid, though, which leaves the json-pointer escape boundary open, and that
// gap does get vectors.
//
// One accepted coverage gap: iri and iri-reference get only the one-way
// containment from uri and uri-reference (containment_test.go) plus the narrow
// ucschar cases in format_iri_ucschar_test.go. Containment cannot catch
// over-acceptance on the ucschar side, and there is no public IRI corpus and no
// stdlib IRI parser to differential against, so that side stays uncovered.

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

var (
	// The RFC 3986 §5.4.1 normal and §5.4.2 abnormal reference-resolution
	// inputs, verbatim and in order. Every one is a well-formed
	// URI-reference; the section exists to pin merge and dot-segment removal,
	// so it is the densest set of relative-reference shapes the RFC publishes
	// and the suite carries none of it.
	rfc3986References = []string{
		// §5.4.1 Normal Examples.
		"g:h", "g", "./g", "g/", "/g", "//g", "?y", "g?y", "#s", "g#s",
		"g?y#s", ";x", "g;x", "g;x?y#s", "", ".", "./", "..", "../", "../g",
		"../..", "../../", "../../g",

		// §5.4.2 Abnormal Examples.
		"../../../g", "../../../../g", "/./g", "/../g", "g.", ".g", "g..",
		"..g", "./../g", "./g/.", "g/./h", "g/../h", "g;x=1/./y", "g;x=1/../y",
		"g?y/./x", "g?y/../x", "g#s/./x", "g#s/../x", "http:g",
	}

	// The distinct §5.4 resolution targets. Each is an absolute URI, so each
	// must satisfy the uri format as well as uri-reference: a reference that
	// resolves against "http://a/b/c/d;p?q" and produces something the uri
	// format rejects would make the two formats disagree about the same string.
	rfc3986ResolvedTargets = []string{
		"g:h", "http://a/b/c/g", "http://a/b/c/g/", "http://a/g", "http://g",
		"http://a/b/c/d;p?y", "http://a/b/c/g?y", "http://a/b/c/d;p?q#s",
		"http://a/b/c/g#s", "http://a/b/c/g?y#s", "http://a/b/c/;x",
		"http://a/b/c/g;x", "http://a/b/c/g;x?y#s", "http://a/b/c/d;p?q",
		"http://a/b/c/", "http://a/b/", "http://a/b/g", "http://a/",
		"http://a/b/c/g.", "http://a/b/c/.g", "http://a/b/c/g..",
		"http://a/b/c/..g", "http://a/b/c/g/h", "http://a/b/c/h",
		"http://a/b/c/g;x=1/y", "http://a/b/c/y", "http://a/b/c/g?y/./x",
		"http://a/b/c/g?y/../x", "http://a/b/c/g#s/./x", "http://a/b/c/g#s/../x",
		"http:g",
	}
)

// TestURIReferenceResolutionVectors runs every RFC 3986 §5.4 reference-
// resolution input through the uri-reference format, and the two that carry a
// scheme through the uri format as well. §5.4 is the RFC's own inventory of
// relative-reference shapes -- dot segments, empty references, semicolon
// parameters, a query or fragment carrying what looks like a path -- and none
// of it appears in the vendored suite.
func TestURIReferenceResolutionVectors(t *testing.T) {
	t.Parallel()

	cases := make(map[string]formatVector, len(rfc3986References))
	for _, ref := range rfc3986References {
		cases["reference "+ref] = formatVector{instance: ref, valid: true}
	}

	runFormatVectors(t, "uri-reference", cases)

	// URI-reference = URI / relative-ref, and a relative-ref cannot begin with
	// a scheme, so exactly the two schemed inputs are absolute URIs.
	runFormatVectors(t, "uri", map[string]formatVector{
		"schemed reference g:h":      {"g:h", true},
		"schemed reference http:g":   {"http:g", true},
		"dot-segment reference":      {"../../g", false},
		"network-path reference //g": {"//g", false},
	})
}

// TestURIResolutionTargetVectors runs every RFC 3986 §5.4 resolution target
// through the uri format. These are the strings a caller holds after resolving
// a reference against a base, so the format must accept them if the resolution
// step is to be usable.
func TestURIResolutionTargetVectors(t *testing.T) {
	t.Parallel()

	cases := make(map[string]formatVector, len(rfc3986ResolvedTargets))
	for _, target := range rfc3986ResolvedTargets {
		cases["target "+target] = formatVector{instance: target, valid: true}
	}

	runFormatVectors(t, "uri", cases)
}

// TestDateTimeOffsetVectors checks the date-time format against RFC 3339 §4.3,
// which gives "-00:00" a meaning "+00:00" does not: the offset is unknown, as
// distinct from known to be UTC. Both are well-formed time-offset productions
// and the suite covers neither.
func TestDateTimeOffsetVectors(t *testing.T) {
	t.Parallel()

	runFormatVectors(t, "date-time", map[string]formatVector{
		"negative zero offset (RFC 3339 §4.3)": {"2020-01-02T03:04:05-00:00", true},
		"positive zero offset":                 {"2020-01-02T03:04:05+00:00", true},
		"Z designator":                         {"2020-01-02T03:04:05Z", true},
		"negative offset":                      {"2020-01-02T03:04:05-05:00", true},
		"offset hour out of range":             {"2020-01-02T03:04:05-24:00", false},
		"offset minute out of range":           {"2020-01-02T03:04:05-00:60", false},
		"offset without minutes":               {"2020-01-02T03:04:05-05", false},
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
		"empty class":            {"[]", true},
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

// vectorDir holds one acceptance-vector file per format, each file named for
// the format it covers.
const vectorDir = "testdata/vectors"

// TestFormatVectors runs every vector file under testdata/vectors through the
// validator its basename names. Adding a file adds a test with no Go edit. A
// basename that names no registered format fails inside runFormatVectors, so a
// renamed format cannot leave an orphaned file sitting unexercised.
func TestFormatVectors(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(vectorDir)
	require.NoError(t, err, "read vector directory %s", vectorDir)

	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".tsv")
		require.True(t, ok, "vector file %s must carry the .tsv extension", entry.Name())

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runFormatVectors(t, name, loadVectorFile(t, filepath.Join(vectorDir, entry.Name())))
		})
	}
}

// loadVectorFile reads an acceptance-vector file: three tab-separated fields per
// row, "<input>", <valid>, <note>, with '#' comment lines and blank lines
// skipped. The map is keyed by the raw quoted field, which is printable by
// construction and unique once a duplicate fails the load, so it names the
// subtest without mangling an invisible character.
//
// The input is a Go quoted string literal, which is what lets a row carry the
// empty string, a tab, an invisible joiner, or a lone invalid UTF-8 byte. Write
// it in the spelling strconv.Quote produces: printable non-ASCII stays literal,
// so "münchen.de" is the intended form rather than "m\u00fcnchen.de".
//
// Every field is mandatory. A note that rots to empty, a validity column that
// reads neither "true" nor "false", and an input that repeats an earlier row all
// fail the load, because each of the three would otherwise degrade a row into a
// weaker test that still passes. The note documents the row for whoever reads
// the file; the failure messages identify a row by its input.
func loadVectorFile(t *testing.T, path string) map[string]formatVector {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read vector file %s", path)

	cases := make(map[string]formatVector)
	seen := make(map[string]int)

	scanner := bufio.NewScanner(strings.NewReader(string(data)))

	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		fields := strings.SplitN(text, "\t", 3)
		require.Len(t, fields, 3, "%s:%d: want three tab-separated fields in %q", path, line, text)

		quoted, validText, note := fields[0], fields[1], fields[2]
		require.NotEmpty(t, note, "%s:%d: the note field is mandatory", path, line)

		input, err := strconv.Unquote(quoted)
		require.NoError(t, err, "%s:%d: input %s is not a Go quoted string", path, line, quoted)

		require.Contains(t, []string{"true", "false"}, validText,
			"%s:%d: validity must be true or false", path, line)

		prior, duplicate := seen[input]
		require.False(t, duplicate, "%s:%d: input %s repeats the row on line %d", path, line, quoted, prior)

		seen[input] = line

		cases[quoted] = formatVector{instance: input, valid: validText == "true"}
	}

	require.NoError(t, scanner.Err(), "scan vector file %s", path)
	require.NotEmpty(t, cases, "vector file %s carries no rows", path)

	return cases
}
