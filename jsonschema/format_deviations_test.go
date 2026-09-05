package jsonschema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// The format-deviations table. Where a grammar admits more than one reading,
// the built-in format checkers take one position, and doc.go and README.md
// describe each one. Prose alone is a weak contract, so each row here runs
// the examples the prose quotes through the public API.
//
// Breadth stays where it already lives: format_regex_ascii_escape_test.go,
// format_uritemplate_literals_test.go, format_uri_ipvfuture_test.go, and
// internal/format. The exception is the email row. Nothing else asserts its
// size limits, so that row carries them all.

// deviationCase is one instance run through one format's checker.
type deviationCase struct {
	format   string
	instance string
	want     bool
}

// formatDeviations holds one row per documented deviation, keyed by the format
// the deviation concerns.
var formatDeviations = map[string][]deviationCase{
	"regex": {
		{format: "regex", instance: `\a`, want: true},
		{format: "regex", instance: `\_`, want: true},
		{format: "regex", instance: `\c`, want: true},
	},
	"uri-template": {
		{format: "uri-template", instance: "{=path}", want: true},
		{format: "uri-template", instance: "{!x}", want: true},
		{format: "uri-template", instance: "{|x*}", want: true},
		{format: "uri-template", instance: "{keys:1}", want: true},
		{format: "uri-template", instance: "http://example.com/o'reilly", want: true},
	},
	"email": emailSizeLimitCases(),
	"uri": {
		{format: "uri", instance: "http://[v7.x]/", want: true},
		{format: "uri-reference", instance: "http://[v7.x]/", want: true},
	},
	"hostname": {
		{format: "hostname", instance: "ab--cd.com", want: true},
		{format: "idn-hostname", instance: "ab--cd.com", want: false},
		{format: "idn-email", instance: "a@ab--cd.com", want: true},
	},
}

// emailSizeLimitCases builds the RFC 5321 §4.5.3.1 boundary set the email
// deviation claims. The three limits do not compose the way the prose reads.
// The checker measures the 254-octet forward path before the split, so a
// 253-octet domain forces a total of at least 255 and no address reaches that
// limit through the email format. Only idn-email reaches it, counting the
// domain in its A-label form, which is longer than the U-labels the 254-octet
// check saw. The last two rows are that case and its complement.
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

// TestFormatDeviationsBehave runs each row's vectors through the public
// validator, so a deviation that quietly reverses fails here rather than
// surviving as a sentence nothing checks.
func TestFormatDeviationsBehave(t *testing.T) {
	t.Parallel()

	for name, cases := range formatDeviations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, tc := range cases {
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
