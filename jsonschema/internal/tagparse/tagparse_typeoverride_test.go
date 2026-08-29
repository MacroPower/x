package tagparse_test

import (
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
)

// TestApplyTypeOverrideContentKeywords pins that a type= override drops the
// string-only content keywords (contentEncoding, contentMediaType,
// contentSchema) when the new type is not string, matching the treatment of
// the other kind-derived string keywords such as format. The generator emits
// contentEncoding=base64 for []byte fields, so without the drop a type=array
// override ships a non-string schema carrying a vacuous string annotation.
func TestApplyTypeOverrideContentKeywords(t *testing.T) {
	t.Parallel()

	contentSchema := &jsonschema.Schema{Type: "object"}

	tests := map[string]struct {
		tag                  string
		wantContentEncoding  string
		wantContentMediaType string
		wantContentSchema    *jsonschema.Schema
	}{
		"non-string override clears content keywords": {
			tag: "type=array",
		},
		"string override keeps content keywords": {
			tag:                  "type=string",
			wantContentEncoding:  "base64",
			wantContentMediaType: "application/json",
			wantContentSchema:    contentSchema,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The payload shape byteSliceSchema produces (plus the optional
			// media-type keywords a hook may add) before the tag applies. A type=
			// override restructures the payload, so the content keywords live there.
			payload := &jsonschema.Schema{
				Type:             "string",
				ContentEncoding:  "base64",
				ContentMediaType: "application/json",
				ContentSchema:    contentSchema,
			}

			_, err := tagparse.Apply(tagparse.Input{
				Tag:       tc.tag,
				FieldType: reflect.TypeFor[[]byte](),
				Canvas:    &jsonschema.Schema{},
				Payload:   payload,
			})
			require.NoError(t, err)

			assert.Equal(t, tc.wantContentEncoding, payload.ContentEncoding)
			assert.Equal(t, tc.wantContentMediaType, payload.ContentMediaType)
			assert.Equal(t, tc.wantContentSchema, payload.ContentSchema)
		})
	}
}
