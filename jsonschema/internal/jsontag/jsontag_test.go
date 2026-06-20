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

func sampleField(t *testing.T, name string) reflect.StructField {
	t.Helper()

	f, ok := reflect.TypeFor[tagSample]().FieldByName(name)
	require.True(t, ok)

	return f
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		field string
		want  jsontag.Info
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
		"excluded":            {field: "Excluded", want: jsontag.Info{}},
		"dash name":           {field: "DashName", want: jsontag.Info{JSONName: "-", TaggedName: true}},
		"no tag":              {field: "Plain", want: jsontag.Info{JSONName: "Plain"}},
		"unexported excluded": {field: "hidden", want: jsontag.Info{}},
		"anonymous type name": {field: "tagEmbed", want: jsontag.Info{JSONName: "tagEmbed"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, jsontag.Parse(sampleField(t, tc.field)))
		})
	}
}
