package jsonschema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// Edge-case tests for the built-in format validators in validate_formats.go.

func TestRegexFormatAcceptsECMA262Constructs(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "regex",
	}

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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
			if tc.valid {
				require.NoError(t, err,
					"valid ECMA 262 regex should pass format=regex validation")
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

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "regex",
	}

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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
			if tc.valid {
				require.NoError(t, err,
					"valid ECMA 262 regex should pass format=regex validation")
			} else {
				require.Error(t, err,
					"invalid ECMA 262 regex should be rejected by format=regex validation")
			}
		})
	}
}

func TestEmailFormatAcceptsQuotedLocalAndAddressLiteral(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "email",
	}

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
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
			if tc.valid {
				require.NoError(t, err,
					"valid RFC 5321 email should pass format=email validation")
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

	emailSchema := &jsonschema.Schema{
		Type:   "string",
		Format: "email",
	}
	idnEmailSchema := &jsonschema.Schema{
		Type:   "string",
		Format: "idn-email",
	}
	hostnameSchema := &jsonschema.Schema{
		Type:   "string",
		Format: "hostname",
	}
	idnHostnameSchema := &jsonschema.Schema{
		Type:   "string",
		Format: "idn-hostname",
	}

	err := jsonschema.Validate(emailSchema, "user@example.123", jsonschema.WithFormats(true))
	require.NoError(t, err,
		"email with an all-numeric TLD should be accepted (RFC 5321 sub-domain grammar)")

	err = jsonschema.Validate(idnEmailSchema, "user@example.123", jsonschema.WithFormats(true))
	require.NoError(t, err,
		"idn-email with an all-numeric TLD should be accepted (RFC 6531/5321)")

	// The same domain stays invalid for the hostname and idn-hostname formats,
	// which ban an all-numeric TLD to disambiguate from an IPv4 address.
	err = jsonschema.Validate(hostnameSchema, "example.123", jsonschema.WithFormats(true))
	require.Error(t, err,
		"hostname with an all-numeric TLD should be rejected")

	err = jsonschema.Validate(idnHostnameSchema, "example.123", jsonschema.WithFormats(true))
	require.Error(t, err,
		"idn-hostname with an all-numeric TLD should be rejected")
}

func TestValidateDateExtendedYear(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "date",
	}

	// RFC 3339 full-date requires exactly 4-digit year.
	err := jsonschema.Validate(schema, "12345-06-19", jsonschema.WithFormats(true))
	require.Error(t, err,
		"extended year (>4 digits) should be rejected for format=date")
}

func TestValidateDateTimeExtendedYear(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "date-time",
	}

	err := jsonschema.Validate(schema, "12345-01-01T00:00:00Z", jsonschema.WithFormats(true))
	require.Error(t, err,
		"extended year (>4 digits) should be rejected for format=date-time")
}

func TestHostnameRejectsTrailingDotFQDN(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "hostname",
	}

	// RFC 1123 and RFC 5890 allow trailing dots in FQDNs.
	err := jsonschema.Validate(schema, "example.com.", jsonschema.WithFormats(true))
	require.NoError(t, err,
		"trailing-dot FQDN should be accepted for format=hostname")
}

func TestHostnameRejectsAllNumericTLD(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "hostname",
	}

	// An all-numeric top-level label is rejected to disambiguate from an IPv4
	// address (RFC 1123 2.1).
	err := jsonschema.Validate(schema, "999.999.999", jsonschema.WithFormats(true))
	require.Error(t, err,
		"all-numeric hostname should be rejected for format=hostname")
}

func TestIDNHostnameRejectsOver253Octets(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "idn-hostname",
	}

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
	err := jsonschema.Validate(schema, long.String(), jsonschema.WithFormats(true))
	require.Error(t, err,
		"idn-hostname exceeding 253 octets should be rejected")
}

func TestIDNEmailRejectsOver64OctetLocalPart(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "idn-email",
	}

	// Local part with 65 characters exceeds the 64-octet limit.
	longLocal := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklm" // 65 chars
	err := jsonschema.Validate(schema, longLocal+"@example.com", jsonschema.WithFormats(true))
	require.Error(t, err,
		"idn-email with >64 octet local part should be rejected")
}

func TestIDNEmailRejectsSpacesInLocalPart(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "idn-email",
	}

	// Spaces in local part should be rejected per RFC 6531/5321.
	err := jsonschema.Validate(schema, "user name with spaces@example.com", jsonschema.WithFormats(true))
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

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "idn-email",
	}

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
		// Existing idn-email suite expectations stay green.
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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
			if tc.want {
				require.NoError(t, err,
					"valid RFC 6531 idn-email should pass format=idn-email validation")
			} else {
				require.Error(t, err,
					"invalid RFC 6531 idn-email should be rejected by format=idn-email validation")
			}
		})
	}
}

func TestURITemplateMinimalValidation(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "uri-template",
	}

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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
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

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "uri-template",
	}

	tests := map[string]struct {
		instance string
		valid    bool
	}{
		// The brace expression is the whole instance so the grammar is tested
		// directly; literal-only templates are covered by the suite.
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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
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

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "uri",
	}

	// Tab character in URI should be rejected per RFC 3986.
	err := jsonschema.Validate(schema, "http://example.com/path\twith\ttabs", jsonschema.WithFormats(true))
	require.Error(t, err,
		"URI with control characters should be rejected")
}

func TestIRIRejectsDoubleQuote(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "iri",
	}

	// Double quote in IRI should be rejected per RFC 3987 (inherits from RFC 3986).
	err := jsonschema.Validate(schema, `http://example.com/path"with"quotes`, jsonschema.WithFormats(true))
	require.Error(t, err,
		"IRI with double quotes should be rejected")
}

func TestURIRejectsInvalidPercentEncoding(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "uri",
	}

	// %ZZ is not valid percent-encoding (Z is not a hex digit).
	err := jsonschema.Validate(schema, "http://example.com/%ZZ", jsonschema.WithFormats(true))
	require.Error(t, err,
		"URI with invalid percent-encoding should be rejected")
}

func TestHostnameRejectsFullwidthFullStop(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "hostname",
	}

	// A fullwidth full stop (U+FF0E) is not an ASCII label separator and is
	// rejected.
	err := jsonschema.Validate(schema, "example\uFF0Ecom", jsonschema.WithFormats(true))
	require.Error(t, err)
}

func TestTimeRequiresOffset(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "time",
	}

	// RFC 3339 requires a zone offset; an offsetless time is rejected.
	err := jsonschema.Validate(schema, "12:00:00", jsonschema.WithFormats(true))
	require.Error(t, err,
		"time without offset should be rejected (RFC 3339 requires offset)")
}

func TestURIRejectsMalformedForms(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "uri",
	}

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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
			require.Error(t, err,
				"malformed URI should be rejected by format=uri")
		})
	}
}

// TestURIAcceptsEmptyPathSegments covers RFC 3986 §3.3 path-abempty, which
// permits empty path segments. Consecutive slashes after the authority are
// valid empty segments, not a malformed authority/path boundary.
func TestURIAcceptsEmptyPathSegments(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "uri",
	}

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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
			require.NoError(t, err,
				"URI with empty path segments should be accepted for format=uri")
		})
	}
}

func TestIRIRejectsInvalidPercentEncoding(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "iri",
	}

	// %ZZ is not valid percent-encoding.
	err := jsonschema.Validate(schema, "http://example.com/%ZZ", jsonschema.WithFormats(true))
	require.Error(t, err,
		"IRI with invalid percent-encoding should be rejected")
}

func TestIPv4LeadingZeros(t *testing.T) {
	t.Parallel()

	// Net.ParseIP rejects leading zeros in IPv4 octets (since Go 1.17; this
	// module requires Go 1.24+).
	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "ipv4",
	}

	err := jsonschema.Validate(schema, "01.02.03.04", jsonschema.WithFormats(true))
	require.Error(t, err,
		"IPv4 with leading zeros should be rejected")
}

// TestHostnameInteriorDoubleHyphen covers RFC 1123 labels that carry "--" at
// positions 3-4 without the "xn--" ACE prefix. The hostname format is RFC
// 1123-based and permits interior hyphens, so only true A-labels (the "xn--"
// prefix, matched case-insensitively per RFC 5890) are validated as Punycode.
func TestHostnameInteriorDoubleHyphen(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "hostname",
	}

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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
			if tc.want {
				require.NoError(t, err,
					"RFC 1123 hostname should be accepted for format=hostname")
			} else {
				require.Error(t, err,
					"malformed A-label hostname should be rejected for format=hostname")
			}
		})
	}
}

// TestIDNHostnameRejectsNumericTopLabel mirrors the plain hostname numeric-TLD
// rule: an idn-hostname whose top-level label is all digits is rejected to keep
// it from being confused with an IPv4 address (RFC 1123 §2.1 / RFC 5890).
func TestIDNHostnameRejectsNumericTopLabel(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "idn-hostname",
	}

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

			err := jsonschema.Validate(schema, tc.instance, jsonschema.WithFormats(true))
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

// TestURIRejectsBareIPv6 confirms format=uri rejects an unbracketed IPv6
// authority, matching format=iri so the two stay consistent (RFC 3986 §3.2.2).
func TestURIRejectsBareIPv6(t *testing.T) {
	t.Parallel()

	const bareIPv6 = "http://2001:db8::1/path"

	uriSchema := &jsonschema.Schema{
		Type:   "string",
		Format: "uri",
	}
	iriSchema := &jsonschema.Schema{
		Type:   "string",
		Format: "iri",
	}

	err := jsonschema.Validate(uriSchema, bareIPv6, jsonschema.WithFormats(true))
	require.Error(t, err,
		"uri with an unbracketed IPv6 authority should be rejected")

	err = jsonschema.Validate(iriSchema, bareIPv6, jsonschema.WithFormats(true))
	require.Error(t, err,
		"iri with an unbracketed IPv6 authority should be rejected (consistency check)")

	// A bracketed IPv6 authority remains valid for uri.
	err = jsonschema.Validate(uriSchema, "http://[2001:db8::1]/path", jsonschema.WithFormats(true))
	require.NoError(t, err,
		"uri with a bracketed IPv6 authority should be accepted")
}
