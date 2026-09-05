package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestJSONPointerFallbackThroughDataKeepsDocumentBase pins the base URI of a
// $ref target materialized through the JSON-pointer fallback from inside a
// non-schema keyword. The "$id" data member crossed inside examples is plain
// instance data, not a resource boundary, so the target's own relative $ref
// absolutizes against the containing document's base, not the data object's
// "$id". The resolver serves only the document-base URI; compiling
// successfully proves no other URI was fetched.
func TestJSONPointerFallbackThroughDataKeepsDocumentBase(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{
		"$id": "https://root.example/s.json",
		"examples": [{"$id": "https://models.example/widget", "payload": {"$ref": "sub.json"}}],
		"properties": {"x": {"$ref": "#/examples/0/payload"}}
	}`))
	require.NoError(t, err)

	resolver := jsonschema.RefResolverFunc(func(_ context.Context, uri string) (*jsonschema.Schema, error) {
		if uri == "https://root.example/sub.json" {
			return &jsonschema.Schema{Type: "string"}, nil
		}

		return nil, fmt.Errorf("%w: %s", jsonschema.ErrNotResolved, uri)
	})

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err,
		"the fallback target's relative $ref must resolve against the document base")

	require.NoError(t, v.Validate(t.Context(), map[string]any{"x": "hello"}))
	require.Error(t, v.Validate(t.Context(), map[string]any{"x": 42}),
		"the fetched sub-schema must constrain x to a string")
}

// BenchmarkValidateFallbackEnum validates a 10,000-element array of strings
// against a 50-member string enum, each element matching the last member so
// every evaluation scans the whole enum. The "indexed" variant holds the enum
// under items, where Compile caches its document view by node id. The
// "fallback" variant reaches the same enum through a JSON-pointer ref into an
// unknown keyword (#/examples/0), which a run materializes as a fresh schema
// outside the index, so the enum's document view comes from the run's memo
// rather than the compile-time cache. The ref resolution itself costs the
// fallback variant about two allocations per element over the indexed one;
// a view rebuilt per element adds one more allocation and the 50-member
// conversion (about 900 bytes) on top, so a gap much wider than that means
// the memo stopped holding.
func BenchmarkValidateFallbackEnum(b *testing.B) {
	const (
		enumSize  = 50
		arraySize = 10000
	)

	members := make([]string, enumSize)
	for i := range members {
		members[i] = fmt.Sprintf("member-%02d", i)
	}

	enum, err := json.Marshal(members)
	if err != nil {
		b.Fatal(err)
	}

	elements := make([]string, arraySize)
	for i := range elements {
		elements[i] = members[enumSize-1]
	}

	instance, err := json.Marshal(elements)
	if err != nil {
		b.Fatal(err)
	}

	schemas := map[string]string{
		"indexed": `{"type": "array", "items": {"enum": ` + string(enum) + `}}`,
		"fallback": `{"type": "array", "items": {"$ref": "#/examples/0"}, ` +
			`"examples": [{"enum": ` + string(enum) + `}]}`,
	}

	for name, schema := range schemas {
		b.Run(name, func(b *testing.B) {
			v, err := jsonschema.CompileJSON(b.Context(), []byte(schema))
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()

			// Every element matches, so a non-nil error means the benchmark
			// stopped measuring the full scan.
			for b.Loop() {
				if v.ValidateJSON(b.Context(), instance) != nil {
					b.Fatal("expected the instance to validate")
				}
			}
		})
	}
}

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

// TestCompileChecksJSONPointerFallbackTargets locks in that the compile-time
// structural checks (type names, non-negative bounds, and the Draft-07 items
// array under Draft 2020-12) extend to $ref targets materialized through the
// JSON-pointer fallback: schemas carried inside unknown keywords, which the
// typed root pass never reaches. Without the extension such a target compiles
// cleanly and then silently mis-validates.
func TestCompileChecksJSONPointerFallbackTargets(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		err    error
	}{
		"items array in fallback target": {
			schema: `{"$ref": "#/x", "x": {"type": "array", "items": [{"type": "string"}]}}`,
			err:    jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"negative bound in fallback target": {
			schema: `{"$ref": "#/x", "x": {"minLength": -5}}`,
			err:    jsonschema.ErrNegativeBound,
		},
		"invalid type name in fallback target": {
			schema: `{"$ref": "#/x", "x": {"type": "strng"}}`,
			err:    jsonschema.ErrInvalidType,
		},
		"violation one ref deeper than the first target": {
			// The compile-time reference walk resolves #/x directly and #/y
			// only through x's own $ref, on the pass that ref-walks the
			// targets tolerantly. A refused target is settled, so that pass
			// fails on it too. The checks therefore cover every materialized
			// target, not just the first level.
			schema: `{"$ref": "#/x", "x": {"$ref": "#/y"}, "y": {"maxItems": -1}}`,
			err:    jsonschema.ErrNegativeBound,
		},
		"violation nested inside a fallback target": {
			schema: `{"$ref": "#/x", "x": {"properties": {"a": {"minLength": -3}}}}`,
			err:    jsonschema.ErrNegativeBound,
		},
		"well-formed fallback target compiles": {
			schema: `{"$ref": "#/x", "x": {"type": "string"}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err,
				"a fallback-materialized target must fail the same compile checks as the typed tree")
		})
	}
}

// TestCompileFallbackTargetErrorNamesLocation pins the error path. The error
// names the JSON Pointer that materialized the target, so the offending
// keyword is addressable. It also names the reference that reached the target,
// since the closure walk reports the vet's refusal where the reference
// resolves.
func TestCompileFallbackTargetErrorNamesLocation(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "#/x", "x": {"minLength": -5}}`))
	require.NoError(t, err)

	_, err = jsonschema.Compile(t.Context(), schema)
	require.ErrorIs(t, err, jsonschema.ErrNegativeBound)
	assert.Contains(t, err.Error(), "#/x/minLength",
		"the message names the pointer that materialized the target")
	assert.Contains(t, err.Error(), `cannot resolve $ref "#/x"`,
		"the message names the reference the walk refused the target at")
}
