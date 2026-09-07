package tagmodel_test

import (
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// TestRefClassification pins that a bare $ref base classifies as the
// definition it names, read through the resolver, over the same Go type: the
// reference answers as the inline schema would. Without a readable
// definition it is the unresolved form every rule reports on.
func TestRefClassification(t *testing.T) {
	t.Parallel()

	type object struct{}

	ref := &jsonschema.Schema{Ref: "#/$defs/T"}
	body := func(s *jsonschema.Schema) func() *jsonschema.Schema {
		return func() *jsonschema.Schema { return s }
	}

	tests := map[string]struct {
		typ  reflect.Type
		def  func() *jsonschema.Schema
		want tagmodel.Form
	}{
		"struct over an object body": {
			typ: reflect.TypeFor[object](), def: body(&jsonschema.Schema{Type: "object"}),
			want: tagmodel.FormDeclaredObject,
		},
		"struct over a string body": {
			typ: reflect.TypeFor[object](), def: body(&jsonschema.Schema{Type: "string"}),
			want: tagmodel.FormTextString,
		},
		"int over an integer body": {
			typ: reflect.TypeFor[int](), def: body(&jsonschema.Schema{Type: "integer"}),
			want: tagmodel.FormNumber,
		},
		"pointer to int over an integer body": {
			typ: reflect.TypeFor[*int](), def: body(&jsonschema.Schema{Type: "integer"}),
			want: tagmodel.FormNumber,
		},
		"struct over a body declaring no type": {
			typ: reflect.TypeFor[object](), def: body(&jsonschema.Schema{}),
			want: tagmodel.FormOpaque,
		},
		"no resolver": {
			typ: reflect.TypeFor[object](), def: nil,
			want: tagmodel.FormUnresolvedRef,
		},
		"resolver answering nil": {
			typ: reflect.TypeFor[object](), def: body(nil),
			want: tagmodel.FormUnresolvedRef,
		},
		"body that is itself a reference": {
			typ: reflect.TypeFor[object](), def: body(&jsonschema.Schema{Ref: "#/$defs/U"}),
			want: tagmodel.FormUnresolvedRef,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tagmodel.ShapeOfQuoted(tc.typ, ref, false, tc.def).Form)
		})
	}

	assert.Equal(t, tagmodel.FormUnresolvedRef, tagmodel.ShapeOf(reflect.TypeFor[object](), ref).Form,
		"ShapeOf has no resolver, so a $ref base is unresolved there")
}
