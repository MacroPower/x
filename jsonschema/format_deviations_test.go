package jsonschema_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// The format-deviations ledger. Both doc.go and README.md carry the same
// five-bullet list of positions the built-in format checkers take where a
// grammar admits more than one reading, and the module CLAUDE.md requires the
// two files stay in lockstep. Prose alone is a weak contract. Two of these
// positions were decided twice, once in each direction, because the reading
// lived in a sentence rather than a fixture.
//
// Each ledger row binds one bullet to the behavior it claims. The phrase must
// appear in both files, and the vectors are the bullet's own quoted examples run
// through the public API. The row count must equal the bullet count in both
// lists, which is the check that catches a sixth deviation landing with no test.
//
// Breadth stays where it already lives: format_regex_ascii_escape_test.go,
// format_uritemplate_literals_test.go, format_uri_ipvfuture_test.go, and
// internal/format. The exception is the email
// bullet. Nothing else asserts its size limits, so that row carries them all.

const (
	// The sentence introducing each list, and the heading each list runs up to.
	// The guard counts bullets between them. The README terminator is a bare
	// '#', so a heading of any level closes the window; a deviation bullet
	// indents by two and never matches it.
	deviationListOpening   = "the checker takes the position below"
	deviationDocHeading    = "// #"
	deviationReadmeHeading = "#"

	// The line prefix a bullet takes in each file. A continuation line indents
	// further in doc.go and by two spaces in README.md, so neither matches.
	deviationDocBullet    = "//   - "
	deviationReadmeBullet = "- **"
)

// deviationCase is one instance run through one format's checker.
type deviationCase struct {
	format   string
	instance string
	want     bool
}

// deviation is one bullet of the list: a phrase both files must carry, and the
// behavior the bullet claims.
type deviation struct {
	phrase string
	cases  []deviationCase
}

// formatDeviations holds one row per bullet, keyed by the format the bullet
// leads with. Every phrase is chosen to survive the normalization below, so a
// reflow of either file cannot break it; only a rewording can.
var formatDeviations = map[string]deviation{
	"regex": {
		phrase: "every ASCII character is a valid ECMA-262 Annex B identity escape",
		cases: []deviationCase{
			{format: "regex", instance: `\a`, want: true},
			{format: "regex", instance: `\_`, want: true},
			{format: "regex", instance: `\c`, want: true},
		},
	},
	"uri-template": {
		phrase: "follows errata ID 6937, so an apostrophe is a literal",
		cases: []deviationCase{
			{format: "uri-template", instance: "{=path}", want: true},
			{format: "uri-template", instance: "{!x}", want: true},
			{format: "uri-template", instance: "{|x*}", want: true},
			{format: "uri-template", instance: "{keys:1}", want: true},
			{format: "uri-template", instance: "http://example.com/o'reilly", want: true},
		},
	},
	"email": {
		phrase: "size limits (64 octets local, 253 domain, 254 total)",
		cases:  emailSizeLimitCases(),
	},
	"uri": {
		phrase: "accept an IPvFuture authority",
		cases: []deviationCase{
			{format: "uri", instance: "http://[v7.x]/", want: true},
			{format: "uri-reference", instance: "http://[v7.x]/", want: true},
		},
	},
	"hostname": {
		phrase: "accepts a reserved-LDH label (a hyphen in positions 3 and 4",
		cases: []deviationCase{
			{format: "hostname", instance: "ab--cd.com", want: true},
			{format: "idn-hostname", instance: "ab--cd.com", want: false},
			{format: "idn-email", instance: "a@ab--cd.com", want: true},
		},
	},
}

// emailSizeLimitCases builds the RFC 5321 §4.5.3.1 boundary set the email
// bullet claims. The three limits do not compose the way the bullet reads. The
// checker measures the 254-octet forward path before the split, so a 253-octet
// domain forces a total of at least 255 and no address reaches that limit
// through the email format. Only idn-email reaches it, counting the domain in
// its A-label form, which is longer than the U-labels the 254-octet check saw.
// The last two rows are that case and its complement.
func emailSizeLimitCases() []deviationCase {
	const (
		label63 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		local64 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	// A 189-octet domain, which with the 64-octet local part and the '@' makes
	// a 254-octet address; one octet more makes it 255.
	domain189 := label63 + "." + label63 + "." + strings.Repeat("a", 61)
	domain190 := domain189 + "a"

	// Domains of single-character U-labels, each of which expands to the
	// seven-octet A-label "xn--tda". Thirty labels reach 239 A-label octets and
	// forty reach 319, while both addresses stay far below the 254-octet total.
	shortIDN := strings.TrimSuffix(strings.Repeat("ü.", 30), ".")
	longIDN := strings.TrimSuffix(strings.Repeat("ü.", 40), ".")

	return []deviationCase{
		{format: "email", instance: local64 + "@example.com", want: true},
		{format: "email", instance: local64 + "a@example.com", want: false},
		{format: "email", instance: local64 + "@" + domain189, want: true},
		{format: "email", instance: local64 + "@" + domain190, want: false},
		{format: "idn-email", instance: "a@" + shortIDN, want: true},
		{format: "idn-email", instance: "a@" + longIDN, want: false},
	}
}

// TestFormatDeviationsBehave runs each ledger row's vectors through the public
// validator, so a deviation that quietly reverses fails here rather than
// surviving as a sentence nothing checks.
func TestFormatDeviationsBehave(t *testing.T) {
	t.Parallel()

	for name, dev := range formatDeviations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, tc := range dev.cases {
				t.Run(tc.format+"/"+tc.instance, func(t *testing.T) {
					t.Parallel()

					schema := &jsonschema.Schema{Type: "string", Format: tc.format}

					err := jsonschema.Validate(t.Context(), schema, tc.instance,
						jsonschema.WithFormats(true))
					if tc.want {
						require.NoError(t, err, "%s must accept %q per the %s deviation", tc.format, tc.instance, name)
					} else {
						require.Error(t, err, "%s must reject %q per the %s deviation", tc.format, tc.instance, name)
					}
				})
			}
		})
	}
}

// TestFormatDeviationsDocumented asserts every ledger phrase appears in both
// doc.go and README.md. The two files wrap at different columns and mark up the
// same words differently, so the comparison runs over normalized text rather
// than raw bytes.
func TestFormatDeviationsDocumented(t *testing.T) {
	t.Parallel()

	sources := map[string]string{
		"doc.go":    normalizeDeviationProse(t, "doc.go"),
		"README.md": normalizeDeviationProse(t, "README.md"),
	}

	for name, dev := range formatDeviations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for file, prose := range sources {
				require.Contains(t, prose, dev.phrase,
					"%s no longer carries the %s deviation phrase; the ledger and the prose have drifted", file, name)
			}
		})
	}
}

// TestFormatDeviationsCounted asserts each list holds exactly as many bullets as
// the ledger holds rows. This is the guard that catches a sixth deviation
// landing in the prose with nothing asserting it.
func TestFormatDeviationsCounted(t *testing.T) {
	t.Parallel()

	counts := map[string]int{
		"doc.go":    countDeviationBullets(t, "doc.go", deviationDocHeading, deviationDocBullet),
		"README.md": countDeviationBullets(t, "README.md", deviationReadmeHeading, deviationReadmeBullet),
	}

	for file, count := range counts {
		require.Equal(t, len(formatDeviations), count,
			"%s lists %d format deviations and the ledger holds %d rows", file, count, len(formatDeviations))
	}
}

// normalizeDeviationProse reads a file and reduces it to text the two sources
// can be compared on. It drops comment and list markers, deletes the backtick,
// asterisk, and quote markup, and collapses every whitespace run to one space,
// so a line break inside a phrase cannot hide it.
func normalizeDeviationProse(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)

	var text strings.Builder

	for line := range strings.Lines(string(data)) {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
		trimmed = strings.TrimPrefix(trimmed, "- ")
		text.WriteString(trimmed)
		text.WriteString(" ")
	}

	stripped := strings.NewReplacer("`", "", "*", "", `"`, "").Replace(text.String())

	return strings.Join(strings.Fields(stripped), " ")
}

// countDeviationBullets counts the bullets in one file's deviation list. The
// list runs from the sentence introducing it to the next heading, and only a
// bullet's own first line carries the prefix, since a continuation line indents
// further.
func countDeviationBullets(t *testing.T, path, heading, bullet string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)

	lines := strings.Split(string(data), "\n")

	start := -1

	for i, line := range lines {
		if strings.Contains(line, deviationListOpening) {
			start = i

			break
		}
	}

	require.GreaterOrEqual(t, start, 0,
		"%s no longer carries the sentence opening the deviation list; the count guard cannot find it", path)

	count := 0

	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, heading) {
			return count
		}

		if strings.HasPrefix(line, bullet) {
			count++
		}
	}

	require.Fail(t, "unterminated deviation list", "%s has no heading after the deviation list", path)

	return count
}
