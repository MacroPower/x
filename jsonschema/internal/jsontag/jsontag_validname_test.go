package jsontag_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
)

// nameGrammarSample carries tag names that span encoding/json's name grammar:
// a name is the run of runes before the first comma, backslash, or quote
// character, whatever those runes are. A name cut short by a reserved rune
// other than the comma keeps its leading identifier instead, and is discarded
// in favor of the Go field name when it has none.
type nameGrammarSample struct {
	Emoji     string `json:"a😀b"`
	Space     string `json:"a b"`
	Digits    string `json:"123"`
	Quoted    string `json:"x\"y,omitempty"` //nolint:staticcheck // Intentional: reserved rune in the name.
	Backslash string `json:"x\\y"`           //nolint:staticcheck // Intentional: reserved rune in the name.
	DigitCut  string `json:"1\"x"`           //nolint:staticcheck // Intentional: reserved rune in the name.
	QuoteOpen string `json:"\"q,omitzero"`   //nolint:staticcheck // Intentional: reserved rune opens the name.
}

func TestParse_NameGrammar(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		field string
		want  jsontag.Info
		err   string
	}{
		"emoji kept": {
			field: "Emoji",
			want:  jsontag.Info{JSONName: "a😀b", TaggedName: true},
		},
		"space kept": {
			field: "Space",
			want:  jsontag.Info{JSONName: "a b", TaggedName: true},
		},
		"digits kept": {
			field: "Digits",
			want:  jsontag.Info{JSONName: "123", TaggedName: true},
		},
		"quote keeps leading identifier, reports the cut": {
			field: "Quoted",
			want:  jsontag.Info{JSONName: "x", TaggedName: true, Omitempty: true},
			err:   "before next option",
		},
		"backslash keeps leading identifier, reports the cut": {
			field: "Backslash",
			want:  jsontag.Info{JSONName: "x", TaggedName: true},
			err:   "before next option",
		},
		"cut name without identifier falls back to field name": {
			field: "DigitCut",
			want:  jsontag.Info{JSONName: "DigitCut"},
			err:   "malformed `json` tag",
		},
		"opening quote falls back, options kept": {
			field: "QuoteOpen",
			want:  jsontag.Info{JSONName: "QuoteOpen", Omitzero: true},
			err:   "malformed `json` tag",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, ok := reflect.TypeFor[nameGrammarSample]().FieldByName(tc.field)
			assert.True(t, ok)

			info, err := jsontag.Parse(f)
			assert.Equal(t, tc.want, info)

			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.err)
			}
		})
	}
}

// TestParse_UnprintableNames pins the names a Go source tag literal cannot
// carry, built through [reflect.StructOf]: a control character is an ordinary
// unreserved rune and is kept, and invalid UTF-8 bytes are kept with each
// replaced by the Unicode replacement character, exactly as encoding/json
// does.
func TestParse_UnprintableNames(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		tag  string
		want jsontag.Info
		err  string
	}{
		"control character kept": {
			tag:  "json:\"x\x7fy\"",
			want: jsontag.Info{JSONName: "x\x7fy", TaggedName: true},
		},
		"invalid utf-8 replaced": {
			tag:  "json:\"a\xffb\"",
			want: jsontag.Info{JSONName: "a�b", TaggedName: true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typ := reflect.StructOf([]reflect.StructField{{
				Name: "A",
				Type: reflect.TypeFor[string](),
				Tag:  reflect.StructTag(tc.tag),
			}})

			info, err := jsontag.Parse(typ.Field(0))
			assert.Equal(t, tc.want, info)

			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.err)
			}
		})
	}
}
