package schemashape_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

func TestNullableInnerSchema(t *testing.T) {
	t.Parallel()

	value := &jsonschema.Schema{Type: "string"}

	tests := map[string]struct {
		schema *jsonschema.Schema
		want   *jsonschema.Schema
	}{
		"nil schema": {schema: nil, want: nil},
		"value then null": {
			schema: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{value, {Type: "null"}}},
			want:   value,
		},
		"null then value": {
			schema: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Type: "null"}, value}},
			want:   value,
		},
		"both branches null": {
			schema: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Type: "null"}, {Type: "null"}}},
			want:   nil,
		},
		"no null branch": {
			schema: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{value, {Type: "integer"}}},
			want:   nil,
		},
		"wrong arity": {
			schema: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Type: "null"}}},
			want:   nil,
		},
		"nil branch": {
			schema: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{value, nil}},
			want:   nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := schemashape.NullableInnerSchema(tc.schema)
			if tc.want == nil {
				assert.Nil(t, got)
			} else {
				assert.Same(t, tc.want, got)
			}
		})
	}
}

func TestRelocateConstEnumToValueBranchDegenerateNullAnyOf(t *testing.T) {
	t.Parallel()

	// An anyOf[null, null] has no value branch, so the const stays on the
	// wrapper where it keeps rejecting null, matching the pre-relocation
	// composition.
	s := &jsonschema.Schema{
		AnyOf: []*jsonschema.Schema{{Type: "null"}, {Type: "null"}},
		Const: new(any("abc")),
	}

	got := schemashape.RelocateConstEnumToValueBranch(s)

	assert.Same(t, s, got)
	assert.NotNil(t, s.Const)
	assert.Nil(t, s.AnyOf[0].Const)
	assert.Nil(t, s.AnyOf[1].Const)
}
