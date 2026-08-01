package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestRegexFormatEmptyCharacterClass is a regression test: the regex format is
// defined by ECMA 262, whose CharacterClass production permits an empty
// ClassContents, so "[]" (matches nothing) and "[^]" (matches any character, a
// common idiom) are valid patterns. They were previously rejected as
// unterminated on the mistaken premise that a first-position ']' is a literal
// class member (POSIX bracket-expression behavior, not ECMA 262).
func TestRegexFormatEmptyCharacterClass(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		instance string
		want     bool
	}{
		"empty class":                {instance: "[]", want: true},
		"negated empty class":        {instance: "[^]", want: true},
		"empty class mid-pattern":    {instance: "a[]b", want: true},
		"empty class then literal ]": {instance: "[]]", want: true},
		"unterminated class":         {instance: "[a", want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema := &jsonschema.Schema{Type: "string", Format: "regex"}
			err := jsonschema.Validate(t.Context(), schema, tc.instance,
				jsonschema.WithFormats(true))
			if tc.want {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
