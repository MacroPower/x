package jsonschema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
)

// Format validator tests, originally tracked as TODO.md items.

func TestRegexFormatValidatesRE2NotECMA262(t *testing.T) {
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

func TestEmailFormatUsesRFC5322NotRFC5321(t *testing.T) {
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

func TestHostnameAcceptsAllNumericTLD(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "hostname",
	}

	// All-numeric hostname should be questionable (looks like IPv4).
	// Convention: TLD should not be all-numeric to distinguish from IPv4.
	err := jsonschema.Validate(schema, "999.999.999", jsonschema.WithFormats(true))
	require.Error(t, err,
		"all-numeric hostname should be rejected for format=hostname")
}

func TestIDNHostnameMissing253OctetCheck(t *testing.T) {
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

func TestIDNEmailMissing64OctetLocalPart(t *testing.T) {
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

func TestIDNEmailTooPermissiveLocalPart(t *testing.T) {
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

func TestIRICharsMissingDoubleQuoteCheck(t *testing.T) {
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

func TestURIPercentEncodingNotValidated(t *testing.T) {
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

func TestHostnameRejectsIDNSeparatorsByAccident(t *testing.T) {
	t.Parallel()

	// Non-ASCII characters like \uFF0E (fullwidth full stop) are rejected
	// because they fail the isAlpha/isDigit/isHyphen check, not because
	// they're recognized as IDNA separators.
	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "hostname",
	}

	// The fullwidth stop should be rejected specifically as an IDNA separator,
	// not as an unrecognized character.
	err := jsonschema.Validate(schema, "example\uFF0Ecom", jsonschema.WithFormats(true))
	require.Error(t, err)
}

func TestValidateTimeOffsetRelianceOnTimeParse(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "time",
	}

	// A time without any offset should be rejected.
	// Currently relies on time.Parse failing due to string length mismatch.
	err := jsonschema.Validate(schema, "12:00:00", jsonschema.WithFormats(true))
	require.Error(t, err,
		"time without offset should be rejected (RFC 3339 requires offset)")
}

func TestURIPermissiveValidation(t *testing.T) {
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

func TestIRIPercentEncodingNotValidated(t *testing.T) {
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

	// TODO.md notes this was resolved in Go 1.17+ (module requires Go 1.24+).
	// Go's net.ParseIP correctly rejects leading zeros in IPv4 octets.
	schema := &jsonschema.Schema{
		Type:   "string",
		Format: "ipv4",
	}

	err := jsonschema.Validate(schema, "01.02.03.04", jsonschema.WithFormats(true))
	require.Error(t, err,
		"IPv4 with leading zeros should be rejected")
}
