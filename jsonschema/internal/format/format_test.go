package format_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/format"
)

// Edge-case tests for the built-in format validators, exercised directly via
// [format.Validators]. How the parent package wires the validators into the
// format keyword is covered by its own tests.

// validator returns the built-in validator registered under name, failing the
// test if the format is not registered.
func validator(t *testing.T, name string) func(string) error {
	t.Helper()

	fn, ok := format.Validators()[name]
	require.True(t, ok, "format %q must be registered", name)

	return fn
}

func TestRegexFormatAcceptsECMA262Constructs(t *testing.T) {
	t.Parallel()

	validate := validator(t, "regex")

	tests := map[string]struct {
		instance string
		valid    bool
	}{
		"ECMA262 backreference should be valid": {
			instance: `(foo)\1`,
			valid:    true,
		},
		"ECMA262 lookahead should be valid": {
			instance: `foo(?=bar)`,
			valid:    true,
		},
		"ECMA262 lookbehind should be valid": {
			instance: `(?<=foo)bar`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.valid {
				require.NoError(t, err,
					"valid ECMA 262 regex should pass the regex validator")
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestRegexFormatIdentityEscapesNonASCII covers ECMA 262 Annex B identity
// escapes: a backslash followed by a non-ASCII source character is valid even
// though it names no defined escape. The escaped rune must be decoded as UTF-8
// rather than judged by its lead byte. The ASCII escape rules are unchanged: a
// defined escape like "\d" stays valid and an undefined one like "\a" stays
// invalid.
func TestRegexFormatIdentityEscapesNonASCII(t *testing.T) {
	t.Parallel()

	validate := validator(t, "regex")

	tests := map[string]struct {
		instance string
		valid    bool
	}{
		"escaped multi-byte rune is a valid identity escape": {
			instance: "\\é", // backslash + e-acute
			valid:    true,
		},
		"escaped multi-byte rune mid-pattern is valid": {
			instance: "foo\\ébar",
			valid:    true,
		},
		"escaped emoji is a valid identity escape": {
			instance: "\\\U0001F600", // backslash + grinning face
			valid:    true,
		},
		"escaped literal replacement character is a valid identity escape": {
			instance: "\\�", // backslash + U+FFFD (decodes from 3 bytes)
			valid:    true,
		},
		"escaped lone invalid UTF-8 byte is rejected": {
			instance: "\\\xff", // backslash + a single invalid UTF-8 byte
			valid:    false,
		},
		"escaped ASCII metacharacter stays valid": {
			instance: `\.`,
			valid:    true,
		},
		"escaped ASCII class shorthand stays valid": {
			instance: `\d`,
			valid:    true,
		},
		"undefined ASCII escape stays invalid": {
			instance: `\a`,
			valid:    false,
		},
		"trailing backslash stays invalid": {
			instance: `foo\`,
			valid:    false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.valid {
				require.NoError(t, err,
					"valid ECMA 262 regex should pass the regex validator")
			} else {
				require.Error(t, err,
					"invalid ECMA 262 regex should be rejected by the regex validator")
			}
		})
	}
}

// TestIRIRejectsC1Controls covers RFC 3987: C1 control code points
// (U+0080-U+009F) are below the ucschar range (which begins at U+00A0) and are
// not legal IRI characters. They encode as two bytes >= 0x20, so net/url's
// byte-level control check does not reject them on the IRI path.
func TestIRIRejectsC1Controls(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"iri", "iri-reference"} {
		validate := validator(t, name)
		for _, c := range []rune{0x80, 0x85, 0x9F} {
			require.Error(t, validate("http://example.com/a"+string(c)+"b"),
				"%s with a C1 control U+%04X must be rejected", name, c)
		}

		// A legal ucschar (>= U+00A0) is still accepted.
		require.NoError(t, validate("http://example.com/aéb"),
			"%s with a legal ucschar must be accepted", name)
	}
}

func TestEmailFormatAcceptsQuotedLocalAndAddressLiteral(t *testing.T) {
	t.Parallel()

	validate := validator(t, "email")

	tests := map[string]struct {
		instance string
		valid    bool
	}{
		"quoted local part with special chars": {
			instance: `"user@name"@example.com`,
			valid:    true,
		},
		"IPv6 address literal domain": {
			instance: `user@[IPv6:2001:db8::1]`,
			valid:    true,
		},
		"empty quoted local part": {
			instance: `""@example.com`,
			valid:    false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.valid {
				require.NoError(t, err,
					"valid RFC 5321 email should pass the email validator")
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestEmailAcceptsNumericTLD covers the RFC 5321 §4.1.2 sub-domain grammar
// (sub-domain = Let-dig [Ldh-str]), which permits an all-numeric top-level
// label in an email domain. The numeric-TLD ban belongs to the hostname format
// (RFC 1123 disambiguation), not to email domain validation, so "user@example.123"
// is a valid email even though "example.123" is an invalid hostname.
func TestEmailAcceptsNumericTLD(t *testing.T) {
	t.Parallel()

	err := validator(t, "email")("user@example.123")
	require.NoError(t, err,
		"email with an all-numeric TLD should be accepted (RFC 5321 sub-domain grammar)")

	err = validator(t, "idn-email")("user@example.123")
	require.NoError(t, err,
		"idn-email with an all-numeric TLD should be accepted (RFC 6531/5321)")

	// The same domain stays invalid for the hostname and idn-hostname formats,
	// which ban an all-numeric TLD to disambiguate from an IPv4 address.
	err = validator(t, "hostname")("example.123")
	require.Error(t, err,
		"hostname with an all-numeric TLD should be rejected")

	err = validator(t, "idn-hostname")("example.123")
	require.Error(t, err,
		"idn-hostname with an all-numeric TLD should be rejected")
}

func TestValidateDateExtendedYear(t *testing.T) {
	t.Parallel()

	// RFC 3339 full-date requires exactly 4-digit year.
	err := validator(t, "date")("12345-06-19")
	require.Error(t, err,
		"extended year (>4 digits) should be rejected by the date validator")
}

func TestValidateDateTimeExtendedYear(t *testing.T) {
	t.Parallel()

	err := validator(t, "date-time")("12345-01-01T00:00:00Z")
	require.Error(t, err,
		"extended year (>4 digits) should be rejected by the date-time validator")
}

func TestHostnameAcceptsTrailingDotFQDN(t *testing.T) {
	t.Parallel()

	// RFC 1123 and RFC 5890 allow trailing dots in FQDNs.
	err := validator(t, "hostname")("example.com.")
	require.NoError(t, err,
		"trailing-dot FQDN should be accepted by the hostname validator")
}

func TestHostnameRejectsAllNumericTLD(t *testing.T) {
	t.Parallel()

	// An all-numeric top-level label is rejected to disambiguate from an IPv4
	// address (RFC 1123 2.1).
	err := validator(t, "hostname")("999.999.999")
	require.Error(t, err,
		"all-numeric hostname should be rejected by the hostname validator")
}

func TestIDNHostnameRejectsFullwidthNumericTLD(t *testing.T) {
	t.Parallel()

	// A top-level label of fullwidth digits IDNA-maps to ASCII digits
	// (U+FF11 U+FF12 U+FF13 -> "123"), so it is indistinguishable from an IPv4
	// label and must be rejected like its ASCII spelling. The ban checks the
	// A-label form, not the raw U-label, which isAllDigits would miss.
	fullwidth := "１２３" // fullwidth "123"

	err := validator(t, "idn-hostname")(fullwidth)
	require.Error(t, err,
		"idn-hostname with a fullwidth-digit TLD should be rejected")

	err = validator(t, "idn-hostname")("example." + fullwidth)
	require.Error(t, err,
		"idn-hostname with a fullwidth-digit TLD on a multi-label name should be rejected")
}

func TestIDNHostnameRejectsALabelContextualViolation(t *testing.T) {
	t.Parallel()

	// An A-label that decodes to an RFC 5892 contextual-rule violation must be
	// rejected just like its U-label spelling. The label xn--aa-0ea decodes to
	// "a<MIDDLE DOT>a" (U+00B7 not between two 'l'), and xn--a-jib decodes to a
	// GREEK KERAIA (U+0375) not followed by a Greek character. ToASCII leaves
	// these ASCII A-labels untouched, so the contextual check must run on the
	// decoded U-label, not the raw A-label.
	for _, host := range []string{"xn--aa-0ea.com", "xn--a-jib.com"} {
		err := validator(t, "idn-hostname")(host)
		require.Error(t, err,
			"A-label %q encoding a contextual-rule violation should be rejected", host)
	}

	// The same bypass reaches idn-email domains via validateIDNHostnameLabels.
	err := validator(t, "idn-email")("user@xn--aa-0ea.com")
	require.Error(t, err,
		"idn-email with a contextual-violating A-label domain should be rejected")

	// A valid A-label (xn--mnchen-3ya = "muenchen") still validates.
	err = validator(t, "idn-hostname")("xn--mnchen-3ya.de")
	require.NoError(t, err,
		"a valid IDN A-label should still be accepted")
}

func TestIDNHostnameRejectsOver253Octets(t *testing.T) {
	t.Parallel()

	// Create a hostname that exceeds 253 octets in A-label form.
	// Each label is under 63 chars but total exceeds 253.
	var long strings.Builder

	for i := range 30 {
		if i > 0 {
			long.WriteString(".")
		}

		long.WriteString("abcdefgh")
	}

	// This is 30*8 + 29 dots = 269 characters, exceeding 253 octets.
	err := validator(t, "idn-hostname")(long.String())
	require.Error(t, err,
		"idn-hostname exceeding 253 octets should be rejected")
}

func TestIDNEmailRejectsOver64OctetLocalPart(t *testing.T) {
	t.Parallel()

	// Local part with 65 characters exceeds the 64-octet limit.
	longLocal := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklm" // 65 chars
	err := validator(t, "idn-email")(longLocal + "@example.com")
	require.Error(t, err,
		"idn-email with >64 octet local part should be rejected")
}

func TestIDNEmailRejectsSpacesInLocalPart(t *testing.T) {
	t.Parallel()

	// Spaces in local part should be rejected per RFC 6531/5321.
	err := validator(t, "idn-email")("user name with spaces@example.com")
	require.Error(t, err,
		"spaces in idn-email local part should be rejected")
}

// TestIDNEmailSharesEmailMachinery covers the RFC 6531 idn-email cases that the
// plain email machinery already handles correctly: a quoted local part is split
// at the quote-aware boundary rather than the last '@', its interior is scanned
// with the same escape-aware loop (rejecting control characters, bare interior
// quotes, and a backslash-escaped closing quote), and a bracketed address
// literal in the domain follows the same path the email format uses. The
// RFC 6531 difference is that non-ASCII UTF-8 is permitted in both the quoted
// and unquoted local part.
func TestIDNEmailSharesEmailMachinery(t *testing.T) {
	t.Parallel()

	validate := validator(t, "idn-email")

	tests := map[string]struct {
		instance string
		want     bool
	}{
		// Finding A: split on the quote-aware boundary, not the last '@'.
		"quoted local part containing @ is split at the closing quote": {
			instance: `"x@"@example.com`,
			want:     true,
		},
		"quoted local part with trailing @ in the domain is invalid": {
			instance: `"x@"@host@evil`,
			want:     false,
		},
		// Finding B: scan the quoted interior with the escape-aware loop.
		"control character in quoted local part is invalid": {
			instance: "\"ab\x01cd\"@example.com",
			want:     false,
		},
		"unescaped interior quote in quoted local part is invalid": {
			instance: `"a"b"@example.com`,
			want:     false,
		},
		"backslash-escaped closing quote leaves the local part unterminated": {
			instance: `"weird\"@example.com`,
			want:     false,
		},
		// Finding C: bracketed address literals follow the email-domain path.
		"IPv4 address literal in the domain is valid": {
			instance: "joe@[127.0.0.1]",
			want:     true,
		},
		"IPv6 address literal in the domain is valid": {
			instance: "joe@[IPv6:::1]",
			want:     true,
		},
		"out-of-range IPv4 address literal is invalid": {
			instance: "joe@[999.0.0.1]",
			want:     false,
		},
		// RFC 6531: non-ASCII UTF-8 is permitted in the local part.
		"non-ASCII unquoted local part is valid": {
			instance: "실례@실례.테스트",
			want:     true,
		},
		"non-ASCII quoted local part is valid": {
			instance: `"실례"@example.com`,
			want:     true,
		},
		"a valid plain email is valid": {
			instance: "joe.bloggs@example.com",
			want:     true,
		},
		"a bare integer-like string is invalid": {
			instance: "2962",
			want:     false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"valid RFC 6531 idn-email should pass the idn-email validator")
			} else {
				require.Error(t, err,
					"invalid RFC 6531 idn-email should be rejected by the idn-email validator")
			}
		})
	}
}

func TestURITemplateMinimalValidation(t *testing.T) {
	t.Parallel()

	validate := validator(t, "uri-template")

	tests := map[string]struct {
		instance string
		valid    bool
	}{
		"invalid characters in expression": {
			instance: "http://example.com/{not valid!!!}",
			valid:    false,
		},
		"empty expression": {
			instance: "http://example.com/{}",
			valid:    false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err,
					"invalid URI template should be rejected")
			}
		})
	}
}

// TestURITemplateExprGrammar exercises the RFC 6570 expression grammar
// structurally: an operator-only expression, an empty or malformed varspec, and
// an out-of-range prefix length are rejected, while well-formed expressions with
// operators, prefix and explode modifiers, dotted varnames, and pct-encoded
// varchars are accepted.
func TestURITemplateExprGrammar(t *testing.T) {
	t.Parallel()

	validate := validator(t, "uri-template")

	tests := map[string]struct {
		instance string
		valid    bool
	}{
		// The brace expression is the whole instance so the grammar is tested
		// directly; literal-only templates are covered by the official JSON
		// Schema Test Suite, run from the parent package.
		"simple varname":               {instance: "{a}", valid: true},
		"reserved-expansion operator":  {instance: "{+a}", valid: true},
		"fragment operator with list":  {instance: "{#a,b}", valid: true},
		"prefix length one":            {instance: "{a:1}", valid: true},
		"prefix length max four":       {instance: "{a:9999}", valid: true},
		"explode modifier":             {instance: "{a*}", valid: true},
		"dotted varname":               {instance: "{a.b}", valid: true},
		"pct-encoded varchar":          {instance: "{%20}", valid: true},
		"path-style list with explode": {instance: "{;x,y*}", valid: true},

		"operator without varspec":    {instance: "{|}", valid: false},
		"empty trailing varspec":      {instance: "{a,}", valid: false},
		"empty prefix length":         {instance: "{a:}", valid: false},
		"prefix length five digits":   {instance: "{a:12345}", valid: false},
		"prefix length leading zero":  {instance: "{a:0}", valid: false},
		"operator only":               {instance: "{.}", valid: false},
		"consecutive dots in varname": {instance: "{a..b}", valid: false},
		"characters after explode":    {instance: "{a*b}", valid: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.valid {
				require.NoError(t, err,
					"grammatically valid URI template expression should be accepted")
			} else {
				require.Error(t, err,
					"grammatically invalid URI template expression should be rejected")
			}
		})
	}
}

func TestURICharsControlCharacters(t *testing.T) {
	t.Parallel()

	// Tab character in URI should be rejected per RFC 3986.
	err := validator(t, "uri")("http://example.com/path\twith\ttabs")
	require.Error(t, err,
		"URI with control characters should be rejected")
}

func TestIRIRejectsDoubleQuote(t *testing.T) {
	t.Parallel()

	// Double quote in IRI should be rejected per RFC 3987 (inherits from RFC 3986).
	err := validator(t, "iri")(`http://example.com/path"with"quotes`)
	require.Error(t, err,
		"IRI with double quotes should be rejected")
}

func TestURIRejectsInvalidPercentEncoding(t *testing.T) {
	t.Parallel()

	// %ZZ is not valid percent-encoding (Z is not a hex digit).
	err := validator(t, "uri")("http://example.com/%ZZ")
	require.Error(t, err,
		"URI with invalid percent-encoding should be rejected")
}

func TestHostnameRejectsFullwidthFullStop(t *testing.T) {
	t.Parallel()

	// A fullwidth full stop (U+FF0E) is not an ASCII label separator and is
	// rejected.
	err := validator(t, "hostname")("example\uFF0Ecom")
	require.Error(t, err)
}

func TestTimeRequiresOffset(t *testing.T) {
	t.Parallel()

	// RFC 3339 requires a zone offset; an offsetless time is rejected.
	err := validator(t, "time")("12:00:00")
	require.Error(t, err,
		"time without offset should be rejected (RFC 3339 requires offset)")
}

func TestTimeLeapSecondAnchoredToSecondsField(t *testing.T) {
	t.Parallel()

	validate := validator(t, "time")

	valid := map[string]string{
		"leap second UTC":             "23:59:60Z",
		"leap second explicit offset": "23:59:60+00:00",
		"leap second fractional":      "23:59:60.5Z",
	}

	for name, instance := range valid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, validate(instance))
		})
	}

	// A ":60" in the zone offset's minute field must not be mistaken for a leap
	// second; the offset minute is out of range and the time is rejected.
	invalid := map[string]string{
		"offset minute 60":          "12:30:45+00:60",
		"negative offset minute 60": "12:30:45-00:60",
	}

	for name, instance := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.Error(t, validate(instance),
				"a :60 in the offset minute field is not a leap second")
		})
	}
}

func TestURIRejectsMalformedForms(t *testing.T) {
	t.Parallel()

	validate := validator(t, "uri")

	tests := map[string]struct {
		instance string
	}{
		"missing scheme after colon": {
			instance: "://example.com/path",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			require.Error(t, err,
				"malformed URI should be rejected by the uri validator")
		})
	}
}

// TestURIAcceptsEmptyPathSegments covers RFC 3986 §3.3 path-abempty, which
// permits empty path segments. Consecutive slashes after the authority are
// valid empty segments, not a malformed authority/path boundary.
func TestURIAcceptsEmptyPathSegments(t *testing.T) {
	t.Parallel()

	validate := validator(t, "uri")

	tests := map[string]struct {
		instance string
	}{
		"single empty path segment": {
			instance: "http://host//path",
		},
		"two empty path segments": {
			instance: "http://example.com///a",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			require.NoError(t, err,
				"URI with empty path segments should be accepted by the uri validator")
		})
	}
}

func TestIRIRejectsInvalidPercentEncoding(t *testing.T) {
	t.Parallel()

	// %ZZ is not valid percent-encoding.
	err := validator(t, "iri")("http://example.com/%ZZ")
	require.Error(t, err,
		"IRI with invalid percent-encoding should be rejected")
}

func TestIPv4LeadingZeros(t *testing.T) {
	t.Parallel()

	// Net.ParseIP rejects leading zeros in IPv4 octets (since Go 1.17; this
	// module requires Go 1.24+).
	err := validator(t, "ipv4")("01.02.03.04")
	require.Error(t, err,
		"IPv4 with leading zeros should be rejected")
}

// TestHostnameInteriorDoubleHyphen covers RFC 1123 labels that carry "--" at
// positions 3-4 without the "xn--" ACE prefix. The hostname format is RFC
// 1123-based and permits interior hyphens, so only true A-labels (the "xn--"
// prefix, matched case-insensitively per RFC 5890) are validated as Punycode.
func TestHostnameInteriorDoubleHyphen(t *testing.T) {
	t.Parallel()

	validate := validator(t, "hostname")

	tests := map[string]struct {
		instance string
		want     bool
	}{
		"plain label with leading double hyphen": {
			instance: "ab--cd.com",
			want:     true,
		},
		"plain label with leading double hyphen in subdomain": {
			instance: "te--st.example.com",
			want:     true,
		},
		"valid xn-- A-label decodes as Punycode": {
			instance: "xn--bcher-kva.example.com",
			want:     true,
		},
		"invalid xn-- A-label rejected as bad Punycode": {
			instance: "xn--a.com",
			want:     false,
		},
		"uppercase XN-- A-label still validated as Punycode": {
			instance: "XN--aa---o47jg78q.com",
			want:     false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"RFC 1123 hostname should be accepted by the hostname validator")
			} else {
				require.Error(t, err,
					"malformed A-label hostname should be rejected by the hostname validator")
			}
		})
	}
}

// TestIDNHostnameRejectsNumericTopLabel mirrors the plain hostname numeric-TLD
// rule: an idn-hostname whose top-level label is all digits is rejected to keep
// it from being confused with an IPv4 address (RFC 1123 §2.1 / RFC 5890).
func TestIDNHostnameRejectsNumericTopLabel(t *testing.T) {
	t.Parallel()

	validate := validator(t, "idn-hostname")

	tests := map[string]struct {
		instance string
		want     bool
	}{
		"IPv4-looking idn-hostname": {
			instance: "192.168.1.1",
			want:     false,
		},
		"numeric top-level label": {
			instance: "example.123",
			want:     false,
		},
		"non-numeric top-level label remains valid": {
			instance: "example.com",
			want:     true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"idn-hostname with non-numeric TLD should be accepted")
			} else {
				require.Error(t, err,
					"idn-hostname with all-numeric TLD should be rejected")
			}
		})
	}
}

// TestURIRejectsBareIPv6 confirms the uri validator rejects an unbracketed
// IPv6 authority, matching the iri validator so the two stay consistent
// (RFC 3986 §3.2.2).
func TestURIRejectsBareIPv6(t *testing.T) {
	t.Parallel()

	const bareIPv6 = "http://2001:db8::1/path"

	err := validator(t, "uri")(bareIPv6)
	require.Error(t, err,
		"uri with an unbracketed IPv6 authority should be rejected")

	err = validator(t, "iri")(bareIPv6)
	require.Error(t, err,
		"iri with an unbracketed IPv6 authority should be rejected (consistency check)")

	// A bracketed IPv6 authority remains valid for uri.
	err = validator(t, "uri")("http://[2001:db8::1]/path")
	require.NoError(t, err,
		"uri with a bracketed IPv6 authority should be accepted")
}
