package jsonschema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
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
		"multiple consecutive slashes": {
			instance: "http://example.com///path",
		},
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
