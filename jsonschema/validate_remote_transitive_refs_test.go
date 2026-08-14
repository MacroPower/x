package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestCompileRemoteTransitiveBrokenRef locks in that the resolve-error gate
// vets references transitively: a broken local fragment ref two or more
// ref-hops inside a compile-time-fetched remote document must fail Compile
// like its purely-local and one-hop siblings, instead of compiling into a
// validator whose known-document silent skip then accepts every instance.
func TestCompileRemoteTransitiveBrokenRef(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc     string
		wantErr bool
		// Vacuous marks a document whose ref cycle passes vacuously at
		// validation time (the run-time cycle guard, not this gate), so no
		// instance can be rejected through it.
		vacuous bool
	}{
		"broken ref two hops into the remote fails compile": {
			doc: `{"$defs": {
				"a": {"$ref": "#/$defs/b"},
				"b": {"$ref": "#/$defs/missing"}
			}}`,
			wantErr: true,
		},
		"broken ref three hops into the remote fails compile": {
			doc: `{"$defs": {
				"a": {"$ref": "#/$defs/b"},
				"b": {"$ref": "#/$defs/c"},
				"c": {"$ref": "#nosuchanchor"}
			}}`,
			wantErr: true,
		},
		"resolvable two-hop chain compiles": {
			doc: `{"$defs": {
				"a": {"$ref": "#/$defs/b"},
				"b": {"$ref": "#/$defs/c"},
				"c": {"type": "string"}
			}}`,
		},
		"ref cycle in the remote compiles": {
			doc: `{"$defs": {
				"a": {"$ref": "#/$defs/b"},
				"b": {"anyOf": [{"$ref": "#/$defs/a"}, {"type": "string"}]}
			}}`,
			vacuous: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "https://example.test/doc.json#/$defs/a"}`))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := mapResolver{"https://example.test/doc.json": doc}

			v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			if tc.wantErr {
				require.Error(t, err,
					"a broken ref deep inside a fetched remote must fail compilation, not silently accept")

				return
			}

			require.NoError(t, err)
			require.NoError(t, v.ValidateJSON(t.Context(), []byte(`"hello"`)))

			if !tc.vacuous {
				require.Error(t, v.ValidateJSON(t.Context(), []byte(`123`)),
					"the chain's target must still be enforced")
			}
		})
	}
}
