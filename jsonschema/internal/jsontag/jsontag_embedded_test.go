package jsontag_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
)

// GenBox is a generic named type embedded to exercise the anonymous-field name
// fallback: [reflect.StructField.Name] is the bare identifier "GenBox", while
// [reflect.Type.Name] carries the type arguments ("GenBox[int]").
type GenBox[T any] []T

// embedsGenBox embeds GenBox by value, by pointer, and with an options-only
// tag; encoding/json keys every variant by the field name "GenBox".
type embedsGenBox struct {
	GenBox[int]
}

type embedsGenBoxPtr struct {
	*GenBox[int]
}

type embedsGenBoxTagged struct {
	GenBox[int] `json:",omitempty"`
}

func TestParse_EmbeddedGenericFieldName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		owner reflect.Type
		want  jsontag.Info
	}{
		"value embed": {
			owner: reflect.TypeFor[embedsGenBox](),
			want:  jsontag.Info{JSONName: "GenBox"},
		},
		"pointer embed": {
			owner: reflect.TypeFor[embedsGenBoxPtr](),
			want:  jsontag.Info{JSONName: "GenBox"},
		},
		"options-only tag": {
			owner: reflect.TypeFor[embedsGenBoxTagged](),
			want:  jsontag.Info{JSONName: "GenBox", Omitempty: true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := tc.owner.Field(0)

			info, err := jsontag.Parse(f)
			require.NoError(t, err)
			assert.Equal(t, tc.want, info)
		})
	}
}
