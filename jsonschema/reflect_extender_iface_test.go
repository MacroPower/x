package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// extenderIface is an interface whose method set includes JSONSchemaExtender.
// An interface value cannot be instantiated to call JSONSchemaExtend, so the
// declaration must be skipped and the reflected unrestricted schema kept,
// exactly as an interface declaring JSONSchemaProvider is skipped -- not
// aborted as a provider panic from reflect's nil-interface Method call.
type extenderIface interface {
	jsonschema.JSONSchemaExtender

	Named() string
}

type hasExtenderIfaceField struct {
	F extenderIface `json:"f"`
}

func TestGenerateFor_InterfaceWithExtenderSkipped(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		want     string
	}{
		"root": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[extenderIface](t.Context())
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"$ref":"#/$defs/extenderIface",
				"$defs":{"extenderIface":true}
			}`,
		},
		"struct field": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[hasExtenderIfaceField](t.Context())
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"f":{"$ref":"#/$defs/extenderIface"}},
				"$defs":{"extenderIface":true},
				"required":["f"],
				"additionalProperties":false
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}
