package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURITemplateLiteralCharacters covers the RFC 6570 literals rule for text
// outside brace expressions: any Unicode character except CTL, SP, '"', "'",
// '%' (aside from pct-encoded), '<', '>', '\', '^', '`', '{', '|', and '}',
// with non-ASCII limited to the RFC 3987 ucschar and iprivate sets. The
// official suite has no invalid-literal case, so these are covered here.
func TestURITemplateLiteralCharacters(t *testing.T) {
	t.Parallel()

	validate := validator(t, "uri-template")

	tests := map[string]struct {
		instance string
		want     bool
	}{
		"space in literal":              {instance: "http://e/a b/{x}", want: false},
		"angle brackets in literal":     {instance: "http://e/<a>/{x}", want: false},
		"control character in literal":  {instance: "http://e/a\x01b/{x}", want: false},
		"bare percent in literal":       {instance: "http://e/100%/{x}", want: false},
		"truncated pct-encoded literal": {instance: "http://e/a%2", want: false},
		"non-hex pct-encoded literal":   {instance: "http://e/a%ZZ", want: false},
		"double quote in literal":       {instance: `http://e/a"b"/{x}`, want: false},
		"apostrophe in literal":         {instance: "http://e/a'b/{x}", want: false},
		"backslash in literal":          {instance: `http://e/a\b/{x}`, want: false},
		"pipe in literal":               {instance: "http://e/a|b/{x}", want: false},
		"caret in literal":              {instance: "http://e/a^b/{x}", want: false},
		"noncharacter in literal":       {instance: "http://e/a￿b/{x}", want: false},
		"malformed UTF-8 in literal":    {instance: "http://e/a\xffb/{x}", want: false},

		"plain literal template":        {instance: "http://example.com/dictionary", want: true},
		"literal with expression":       {instance: "http://example.com/{term:1}/x", want: true},
		"pct-encoded literal":           {instance: "http://e/a%20b/{x}", want: true},
		"ucschar literal":               {instance: "http://e/café/{x}", want: true},
		"supplementary ucschar literal": {instance: "http://e/a\U0001F600b/{x}", want: true},
		"iprivate literal":              {instance: "http://e/ab/{x}", want: true},
		"question mark and ampersand":   {instance: "http://e/p?q=1&r=2{&s}", want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"template with valid literals should pass the uri-template validator")
			} else {
				require.Error(t, err,
					"template with invalid literals should be rejected by the uri-template validator")
			}
		})
	}
}
