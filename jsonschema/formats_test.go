package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestBuiltinFormatChecks pins the set of built-in format checkers through
// validation behavior: under WithFormats(true), each built-in format must
// reject a malformed value and accept a well-formed one, so removing or
// breaking a checker is a deliberate, reviewed change. The final case pins
// that an unregistered format name asserts nothing.
func TestBuiltinFormatChecks(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		format  string
		valid   string
		invalid string
	}{
		"date":                  {format: "date", valid: "2024-06-11", invalid: "2024-13-01"},
		"date-time":             {format: "date-time", valid: "2024-06-11T12:00:00Z", invalid: "not-a-date-time"},
		"duration":              {format: "duration", valid: "P1D", invalid: "1D"},
		"email":                 {format: "email", valid: "a@b.example", invalid: "missing-at-sign"},
		"hostname":              {format: "hostname", valid: "example.com", invalid: "a..b"},
		"idn-email":             {format: "idn-email", valid: "a@b.example", invalid: "missing-at-sign"},
		"idn-hostname":          {format: "idn-hostname", valid: "example.com", invalid: "a..b"},
		"ipv4":                  {format: "ipv4", valid: "192.168.0.1", invalid: "256.0.0.1"},
		"ipv6":                  {format: "ipv6", valid: "::1", invalid: "g::1"},
		"iri":                   {format: "iri", valid: "https://example.com/a", invalid: "http://exa mple.com"},
		"iri-reference":         {format: "iri-reference", valid: "/a/b", invalid: "%zz"},
		"json-pointer":          {format: "json-pointer", valid: "/a/b", invalid: "missing-slash"},
		"regex":                 {format: "regex", valid: "^a+$", invalid: "["},
		"relative-json-pointer": {format: "relative-json-pointer", valid: "0/a", invalid: "/starts-with-slash"},
		"time":                  {format: "time", valid: "12:00:00Z", invalid: "25:61:00Z"},
		"uri":                   {format: "uri", valid: "https://example.com", invalid: "http://exa mple.com"},
		"uri-reference":         {format: "uri-reference", valid: "/path", invalid: "%zz"},
		"uri-template":          {format: "uri-template", valid: "{var}", invalid: "{unclosed"},
		"uuid":                  {format: "uuid", valid: "123e4567-e89b-12d3-a456-426614174000", invalid: "not-a-uuid"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{Type: "string", Format: tc.format}

			err := jsonschema.Validate(t.Context(), schema, tc.valid, jsonschema.WithFormats(true))
			require.NoError(t, err, "format %q must accept %q", tc.format, tc.valid)

			err = jsonschema.Validate(t.Context(), schema, tc.invalid, jsonschema.WithFormats(true))
			require.Error(t, err, "format %q must reject %q", tc.format, tc.invalid)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)
			assert.Equal(t, "format", ve.Keyword)
		})
	}

	t.Run("unregistered format asserts nothing", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{Type: "string", Format: "no-such-format"}

		err := jsonschema.Validate(t.Context(), schema, "anything at all", jsonschema.WithFormats(true))
		require.NoError(t, err, "an unregistered format name must be annotation-only")
	})
}
