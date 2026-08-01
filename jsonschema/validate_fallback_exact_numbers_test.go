package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestFallbackTargetConstEnumExact locks in that a $ref target materialized
// through the JSON-pointer fallback (a schema carried inside an unknown
// keyword) keeps const and enum numbers exact beyond float64 precision, like
// every other path a schema document takes into the validator. Without the
// UseNumber decode discipline on the fallback path, 9007199254740993 rounds to
// its float64 neighbor 9007199254740992: the authored value is rejected and
// the neighbor accepted.
func TestFallbackTargetConstEnumExact(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   string
		instance string
		valid    bool
	}{
		"exact const accepted": {
			schema:   `{"myext": {"const": 9007199254740993}, "$ref": "#/myext"}`,
			instance: `9007199254740993`,
			valid:    true,
		},
		"rounded const neighbor rejected": {
			schema:   `{"myext": {"const": 9007199254740993}, "$ref": "#/myext"}`,
			instance: `9007199254740992`,
		},
		"exact enum member accepted": {
			schema:   `{"myext": {"enum": [9007199254740993]}, "$ref": "#/myext"}`,
			instance: `9007199254740993`,
			valid:    true,
		},
		"rounded enum neighbor rejected": {
			schema:   `{"myext": {"enum": [9007199254740993]}, "$ref": "#/myext"}`,
			instance: `9007199254740992`,
		},
		"exact const nested inside fallback target": {
			schema:   `{"myext": {"properties": {"p": {"const": 9007199254740993}}}, "$ref": "#/myext"}`,
			instance: `{"p": 9007199254740993}`,
			valid:    true,
		},
		"rounded const neighbor nested inside fallback target": {
			schema:   `{"myext": {"properties": {"p": {"const": 9007199254740993}}}, "$ref": "#/myext"}`,
			instance: `{"p": 9007199254740992}`,
		},
		"exact const inside examples internals": {
			schema:   `{"examples": [{"const": 9007199254740993}], "$ref": "#/examples/0"}`,
			instance: `9007199254740993`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema))
			require.NoError(t, err)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err, "the exact authored value must satisfy the fallback target")

				return
			}

			require.Error(t, err, "the rounded float64 neighbor must not satisfy the fallback target")
		})
	}
}

// TestFallbackTargetConstExactInLateFetchedDocument covers the remote variant:
// the fallback target sits inside an unknown keyword of a document first
// fetched at validation time, and its const must still compare exactly.
func TestFallbackTargetConstExactInLateFetchedDocument(t *testing.T) {
	t.Parallel()

	doc, err := jsonschema.ParseSchema([]byte(`{"ext": {"const": 9007199254740993}, "$ref": "#/ext"}`))
	require.NoError(t, err)

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "https://example.test/late.json"}`))
	require.NoError(t, err)

	resolver := &lateResolver{doc: doc}

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err, "a resolver miss at compile time is tolerated")

	resolver.armed.Store(true)

	require.NoError(t, v.ValidateJSON(t.Context(), []byte(`9007199254740993`)),
		"the exact authored const must be accepted")
	require.Error(t, v.ValidateJSON(t.Context(), []byte(`9007199254740992`)),
		"the rounded float64 neighbor must be rejected")
}
