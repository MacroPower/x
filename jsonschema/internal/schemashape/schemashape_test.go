package schemashape_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

func TestClearNumericBounds(t *testing.T) {
	t.Parallel()

	s := &jsonschema.Schema{
		Minimum:          new(1.0),
		Maximum:          new(2.0),
		ExclusiveMinimum: new(0.0),
		ExclusiveMaximum: new(3.0),
	}

	schemashape.ClearNumericBounds(s)

	assert.Nil(t, s.Minimum)
	assert.Nil(t, s.Maximum)
	assert.Nil(t, s.ExclusiveMinimum)
	assert.Nil(t, s.ExclusiveMaximum)
}

func TestAdmitsNull(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		want   bool
	}{
		"nil":            {schema: nil, want: false},
		"empty":          {schema: &jsonschema.Schema{}, want: true},
		"null type":      {schema: &jsonschema.Schema{Type: "null"}, want: true},
		"type list null": {schema: &jsonschema.Schema{Types: []string{"null", "string"}}, want: true},
		"anyOf with null": {schema: &jsonschema.Schema{
			AnyOf: []*jsonschema.Schema{{Type: "string"}, {Type: "null"}},
		}, want: true},
		"oneOf with null": {schema: &jsonschema.Schema{
			OneOf: []*jsonschema.Schema{{Type: "null"}, {Type: "string"}},
		}, want: true},
		"plain string": {schema: &jsonschema.Schema{Type: "string"}, want: false},
		"anyOf without null": {schema: &jsonschema.Schema{
			AnyOf: []*jsonschema.Schema{{Type: "string"}, {Type: "integer"}},
		}, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, schemashape.AdmitsNull(tc.schema))
		})
	}
}
