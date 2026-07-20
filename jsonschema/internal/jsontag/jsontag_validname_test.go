package jsontag_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
)

func TestValidName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		tagName string
		want    bool
	}{
		"letters and digits":  {tagName: "abc123", want: true},
		"allowed punctuation": {tagName: "a-b.c_d:e", want: true},
		"space allowed":       {tagName: "a b", want: true},
		"dash alone":          {tagName: "-", want: true},
		"unicode letter":      {tagName: "café", want: true},
		"empty":               {tagName: "", want: false},
		"emoji":               {tagName: "a\U0001F600b", want: false},
		"double quote":        {tagName: `x"y`, want: false},
		"backslash":           {tagName: `x\y`, want: false},
		"control character":   {tagName: "x\u007fy", want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, jsontag.ValidName(tc.tagName))
		})
	}
}

// invalidNameSample carries tag names encoding/json rejects as invalid; the
// field falls back to its Go name and counts as untagged.
type invalidNameSample struct {
	Emoji  string `json:"a😀b"`
	Quoted string `json:"x\"y,omitempty"` //nolint:staticcheck // Intentional: invalid tag name.
}

func TestParse_InvalidTagName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		field string
		want  jsontag.Info
	}{
		"emoji name falls back to field name": {
			field: "Emoji",
			want:  jsontag.Info{JSONName: "Emoji"},
		},
		"quoted name falls back, options kept": {
			field: "Quoted",
			want:  jsontag.Info{JSONName: "Quoted", Omitempty: true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, ok := reflect.TypeFor[invalidNameSample]().FieldByName(tc.field)
			assert.True(t, ok)
			assert.Equal(t, tc.want, jsontag.Parse(f))
		})
	}
}
