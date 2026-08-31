package jsontag_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
)

type tagEmbed struct{ X int }

type tagSample struct {
	Named    int `json:"named"`
	Renamed  int `json:"alias,omitempty"`
	OptOnly  int `json:",omitempty"`
	Multi    int `json:"m,omitzero,string"`
	Excluded int `json:"-"`
	DashName int `json:"-,"` //nolint:staticcheck // intentional: a field literally named "-"
	Plain    int
	hidden   int
	tagEmbed
}

// Reference the reflection-only fields so the unused linter sees them used; the
// tests read them through reflect.
var (
	_ = tagSample{}.hidden
	_ = tagSample{}.X // promoted from the embedded tagEmbed
)

// fieldOf returns the named field of the sample struct T.
func fieldOf[T any](t *testing.T, name string) reflect.StructField {
	t.Helper()

	f, ok := reflect.TypeFor[T]().FieldByName(name)
	require.True(t, ok)

	return f
}

type tagOptionSample struct {
	EmbedOnly     int `json:",embed"`
	EmbedNamed    int `json:"x,embed"`
	EmbedDup      int `json:",embed,embed"` //nolint:staticcheck // Tag under test; the parse error is the expectation.
	FormatPlain   int `json:",format:units"`
	FormatQuoted  int `json:",format:'2006-01-02'"`
	FormatNotLast int `json:",format:x,omitempty"` //nolint:staticcheck // Tag under test; the parse error is the expectation.
	FormatNoValue int `json:",format"`             //nolint:staticcheck // Tag under test; the parse error is the expectation.
	FormatEmpty   int `json:",format:''"`          //nolint:staticcheck // Tag under test; the parse error is the expectation.
	FormatBadRune int `json:",format:9x"`          //nolint:staticcheck // Tag under test; the parse error is the expectation.
	FormatUnended int `json:",format:'abc"`        //nolint:staticcheck // Tag under test; the parse error is the expectation.
}

// TestParseEmbedAndFormat covers the ",embed" and "format:" option grammar:
// the Info fields they set, and the recovered Info beside each parse error.
func TestParseEmbedAndFormat(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		field string
		want  jsontag.Info
		err   string
	}{
		"embed": {
			field: "EmbedOnly",
			want:  jsontag.Info{JSONName: "EmbedOnly", Embed: true},
		},
		"embed with name": {
			// The name parses cleanly here; refusing the combination is the
			// field walk's job, not the tag grammar's.
			field: "EmbedNamed",
			want:  jsontag.Info{JSONName: "x", TaggedName: true, Embed: true},
		},
		"duplicate embed": {
			field: "EmbedDup",
			want:  jsontag.Info{JSONName: "EmbedDup", Embed: true},
			err:   "has duplicate appearance of `embed` tag option",
		},
		"format": {
			field: "FormatPlain",
			want:  jsontag.Info{JSONName: "FormatPlain", Format: "units"},
		},
		"format single-quoted": {
			field: "FormatQuoted",
			want:  jsontag.Info{JSONName: "FormatQuoted", Format: "2006-01-02"},
		},
		"format not last": {
			field: "FormatNotLast",
			want:  jsontag.Info{JSONName: "FormatNotLast", Format: "x", Omitempty: true},
			err:   "has `format` tag option that was not specified last",
		},
		"format missing value": {
			field: "FormatNoValue",
			want:  jsontag.Info{JSONName: "FormatNoValue"},
			err:   "is missing value for `format` tag option",
		},
		"format empty value": {
			field: "FormatEmpty",
			want:  jsontag.Info{JSONName: "FormatEmpty"},
			err:   "cannot have empty value for `format` tag option",
		},
		"format malformed value": {
			field: "FormatBadRune",
			want:  jsontag.Info{JSONName: "FormatBadRune"},
			err:   "has malformed value for `format` tag option",
		},
		"format unterminated quote": {
			field: "FormatUnended",
			want:  jsontag.Info{JSONName: "FormatUnended"},
			err:   "single-quoted string not terminated",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			info, err := jsontag.Parse(fieldOf[tagOptionSample](t, tc.field))
			assert.Equal(t, tc.want, info)

			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.err)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		field string
		want  jsontag.Info
		err   string
	}{
		"explicit name": {field: "Named", want: jsontag.Info{JSONName: "named", TaggedName: true}},
		"name with omitempty": {
			field: "Renamed",
			want:  jsontag.Info{JSONName: "alias", Omitempty: true, TaggedName: true},
		},
		"options only": {field: "OptOnly", want: jsontag.Info{JSONName: "OptOnly", Omitempty: true}},
		"multiple options": {
			field: "Multi",
			want:  jsontag.Info{JSONName: "m", Omitzero: true, JSONString: true, TaggedName: true},
		},
		"excluded": {field: "Excluded", want: jsontag.Info{}},
		"dash name": {
			// The trailing comma names a literal "-" and is also the malformed
			// trailing-comma fault v2 reports; both hold at once.
			field: "DashName",
			want:  jsontag.Info{JSONName: "-", TaggedName: true},
			err:   "invalid trailing ',' character",
		},
		"no tag":              {field: "Plain", want: jsontag.Info{JSONName: "Plain"}},
		"unexported excluded": {field: "hidden", want: jsontag.Info{}},
		"anonymous type name": {field: "tagEmbed", want: jsontag.Info{JSONName: "tagEmbed"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			info, err := jsontag.Parse(fieldOf[tagSample](t, tc.field))
			assert.Equal(t, tc.want, info)

			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.err)
			}
		})
	}
}
