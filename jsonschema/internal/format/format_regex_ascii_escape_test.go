package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegexFormatIdentityEscapesASCII covers the ECMA 262 main-grammar
// IdentityEscape rule for ASCII: IdentityEscape[~UnicodeMode] is any source
// character that is not UnicodeIDContinue, so a backslash before ASCII
// punctuation, space, or a control character is a valid escape (every JS
// engine accepts new RegExp("\\,"), and Go RE2 compiles it too). ASCII
// letters that name no defined escape (e.g. "\a") and "_" are ID_Continue
// characters and stay rejected.
func TestRegexFormatIdentityEscapesASCII(t *testing.T) {
	t.Parallel()

	validate := validator(t, "regex")

	tests := map[string]struct {
		instance string
		want     bool
	}{
		"escaped comma is a valid identity escape":     {instance: `a\,b`, want: true},
		"escaped semicolon is a valid identity escape": {instance: `\;`, want: true},
		"escaped space is a valid identity escape":     {instance: `\ `, want: true},
		"escaped exclamation mark is valid":            {instance: `\!`, want: true},
		"escaped less-than is valid":                   {instance: `\<`, want: true},
		"escaped greater-than is valid":                {instance: `\>`, want: true},
		"escaped equals sign is valid":                 {instance: `\=`, want: true},
		"escaped percent sign is valid":                {instance: `\%`, want: true},
		"escaped double quote is valid":                {instance: `\"`, want: true},
		"escaped single quote is valid":                {instance: `\'`, want: true},
		"escaped ampersand is valid":                   {instance: `\&`, want: true},
		"escaped tilde is valid":                       {instance: `\~`, want: true},
		"escaped hash is valid":                        {instance: `\#`, want: true},
		"escaped colon is valid":                       {instance: `\:`, want: true},
		"escaped at sign is valid":                     {instance: `\@`, want: true},
		"escaped backtick is valid":                    {instance: "\\`", want: true},
		"escaped comma inside a class is valid":        {instance: `[\,]`, want: true},
		"escaped tab is a valid identity escape":       {instance: "\\\t", want: true},

		"escaped metacharacter stays valid":     {instance: `\.`, want: true},
		"escaped class shorthand stays valid":   {instance: `\d`, want: true},
		"undefined letter escape stays invalid": {instance: `\a`, want: false},
		"escaped underscore is invalid":         {instance: `\_`, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.instance)
			if tc.want {
				require.NoError(t, err,
					"valid ECMA 262 regex should pass the regex validator")
			} else {
				require.Error(t, err,
					"invalid ECMA 262 regex should be rejected by the regex validator")
			}
		})
	}
}
