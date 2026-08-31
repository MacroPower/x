package jsonschema_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestInlineSubstituteNestedRefFailurePath(t *testing.T) {
	t.Parallel()

	// A nested ref failure inside a fallback substitute must report a Document
	// and Path that cohere: the Path is the referencing schema's JSON Pointer
	// within the reported document. A substitute that re-bases via its own $id
	// is the root of its own document, so nested failure paths are rooted at
	// ""; a substitute without $id is spliced into the enclosing document, so
	// paths stay rooted at the failing node's location there.
	tests := map[string]struct {
		substitute *jsonschema.Schema
		wantDoc    string
		wantPath   string
	}{
		"substitute with own $id": {
			substitute: &jsonschema.Schema{
				ID:         "https://substitute.example/sub.json",
				Properties: map[string]*jsonschema.Schema{"inner": {Ref: "#/$defs/missing"}},
			},
			wantDoc:  "https://substitute.example/sub.json",
			wantPath: "/properties/inner",
		},
		"substitute without $id": {
			substitute: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"inner": {Ref: "#/$defs/missing"}},
			},
			wantDoc:  "https://root.example/root.json",
			wantPath: "/properties/a/properties/inner",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var captured jsonschema.RefFailure

			fallback := jsonschema.RefFallbackFunc(
				func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
					if f.Ref == "#/$defs/missing" {
						captured = f

						return jsonschema.DropRef()
					}

					return jsonschema.SubstituteRef(tt.substitute)
				},
			)

			root, err := jsonschema.ParseSchema([]byte(stringtest.Input(`
				{
					"$id": "https://root.example/root.json",
					"properties": {"a": {"$ref": "#/$defs/absent"}}
				}
			`)))
			require.NoError(t, err)

			_, err = jsonschema.Inline(t.Context(), root, jsonschema.WithRefFallback(fallback))
			require.NoError(t, err)

			assert.Equal(t, "#/$defs/missing", captured.Ref, "the nested ref is the one captured")
			assert.Equal(t, tt.wantDoc, captured.Document,
				"the failure reports the document containing the failing ref")
			assert.Equal(t, tt.wantPath, captured.Path,
				"the path is the referencing schema's pointer within the reported document")
		})
	}
}
