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
