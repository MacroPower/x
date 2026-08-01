package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestFieldContextEffectiveAccessorsNilCanvas pins that the Effective accessors
// tolerate a caller-built FieldContext with a nil Canvas the same way they
// tolerate a nil Base: the nil side reads as unauthored, so a hand-built
// context (an interpreter unit test driving an accessor directly) falls back
// to the type-derived Base value instead of panicking on a nil dereference.
func TestFieldContextEffectiveAccessorsNilCanvas(t *testing.T) {
	t.Parallel()

	base := &jsonschema.Schema{
		Format:           "email",
		Pattern:          "^a+$",
		ContentEncoding:  "base64",
		ContentMediaType: "application/json",
	}

	tests := map[string]struct {
		fc   jsonschema.FieldContext
		get  func(jsonschema.FieldContext) string
		want string
	}{
		"format falls back to base": {
			fc:   jsonschema.FieldContext{Base: base},
			get:  jsonschema.FieldContext.EffectiveFormat,
			want: "email",
		},
		"pattern falls back to base": {
			fc:   jsonschema.FieldContext{Base: base},
			get:  jsonschema.FieldContext.EffectivePattern,
			want: "^a+$",
		},
		"content encoding falls back to base": {
			fc:   jsonschema.FieldContext{Base: base},
			get:  jsonschema.FieldContext.EffectiveContentEncoding,
			want: "base64",
		},
		"content media type falls back to base": {
			fc:   jsonschema.FieldContext{Base: base},
			get:  jsonschema.FieldContext.EffectiveContentMediaType,
			want: "application/json",
		},
		"format empty when both sides nil": {
			fc:   jsonschema.FieldContext{},
			get:  jsonschema.FieldContext.EffectiveFormat,
			want: "",
		},
		"pattern empty when both sides nil": {
			fc:   jsonschema.FieldContext{},
			get:  jsonschema.FieldContext.EffectivePattern,
			want: "",
		},
		"content encoding empty when both sides nil": {
			fc:   jsonschema.FieldContext{},
			get:  jsonschema.FieldContext.EffectiveContentEncoding,
			want: "",
		},
		"content media type empty when both sides nil": {
			fc:   jsonschema.FieldContext{},
			get:  jsonschema.FieldContext.EffectiveContentMediaType,
			want: "",
		},
		"canvas still wins over base": {
			fc: jsonschema.FieldContext{
				Canvas: &jsonschema.Schema{Format: "uri"},
				Base:   base,
			},
			get:  jsonschema.FieldContext.EffectiveFormat,
			want: "uri",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.get(tc.fc))
		})
	}
}
