package format_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegexFormatIdentityEscapesASCII covers the ECMA 262 Annex B
// IdentityEscape rule for ASCII: SourceCharacterIdentityEscape[~N] is
// "SourceCharacter but not c", so every ASCII character in the escape position
// either names a defined escape or is its own identity escape, and none of them
// rejects. Annex B is normative for web browsers and shipped by every JS engine,
// so the main grammar's narrower "not UnicodeIDContinue" rule would reject
// patterns like "\a" and "\_" that every engine compiles.
//
// The accept cases are kept as regression coverage over the whole ASCII range:
// punctuation, whitespace, control characters, the defined escapes, and the
// letters and underscore the main grammar excludes.
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

		"escaped metacharacter stays valid":         {instance: `\.`, want: true},
		"escaped class shorthand stays valid":       {instance: `\d`, want: true},
		"undefined letter escape is an identity":    {instance: `\a`, want: true},
		"uppercase undefined letter is an identity": {instance: `\A`, want: true},
		"escaped underscore is an identity":         {instance: `\_`, want: true},

		// A backslash with nothing after it is not an escape at all; the
		// structural scan rejects it before the escape rule is consulted.
		"trailing backslash is still invalid": {instance: `\`, want: false},
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
