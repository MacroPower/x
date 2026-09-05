package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateLateFallbackTargetStructuralChecks locks in that a JSON-pointer
// fallback target materialized during a validation run is vetted with the same
// structural checks Compile applies to the fallback targets its gate
// materializes. The target here hides inside an unknown keyword of a document
// first fetched at validation time, so no compile-time pass ever sees it:
// without the per-run vet a negative bound silently never fires and the
// instance validates.
func TestValidateLateFallbackTargetStructuralChecks(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc      string
		instance string
		err      error
		valid    bool
	}{
		"negative bound in fallback target": {
			doc:      `{"x-custom": {"sub": {"minItems": -1}}, "$ref": "#/x-custom/sub"}`,
			instance: `[]`,
			err:      jsonschema.ErrNegativeBound,
		},
		"invalid type name in fallback target": {
			doc:      `{"x-custom": {"sub": {"type": "strng"}}, "$ref": "#/x-custom/sub"}`,
			instance: `"hello"`,
			err:      jsonschema.ErrInvalidType,
		},
		"items array in fallback target under 2020-12": {
			doc:      `{"x-custom": {"sub": {"items": [{"type": "string"}]}}, "$ref": "#/x-custom/sub"}`,
			instance: `[123]`,
			err:      jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"well-formed fallback target validates": {
			doc:      `{"x-custom": {"sub": {"type": "array"}}, "$ref": "#/x-custom/sub"}`,
			instance: `[]`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "https://example.test/late.json"}`))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := &lateResolver{doc: doc}

			v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err, "a resolver miss at compile time is tolerated")

			resolver.armed.Store(true)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.ErrorIs(t, err, jsonschema.ErrRefResolve,
				"an ill-formed fallback target must fail the ref loudly")
			require.ErrorIs(t, err, tc.err,
				"the structural sentinel must stay reachable through the validation error")
		})
	}
}

// TestValidateLateFallbackTargetErrorNamesLocation pins the error path: a
// violation in a fallback target materialized at validation time names the
// document and JSON Pointer that produced it, matching Compile's fallback vet.
func TestValidateLateFallbackTargetErrorNamesLocation(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "https://example.test/late.json"}`))
	require.NoError(t, err)

	doc, err := jsonschema.ParseSchema([]byte(
		`{"x-custom": {"sub": {"minItems": -1}}, "$ref": "#/x-custom/sub"}`,
	))
	require.NoError(t, err)

	resolver := &lateResolver{doc: doc}

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	resolver.armed.Store(true)

	err = v.ValidateJSON(t.Context(), []byte(`[]`))
	require.ErrorIs(t, err, jsonschema.ErrNegativeBound)
	assert.Contains(t, err.Error(), "https://example.test/late.json#/x-custom/sub/minItems")
}

// TestValidateUnresolvableFragmentRefInLateFetchedDocument locks in that an
// unresolvable fragment-only ref inside a document first fetched at validation
// time is reported like its absolute spelling, not silently skipped. The
// silent skip exists because the compile-time reference walk pre-rejects
// broken fragment refs, but a late-fetched document never passes through that
// check: without the guard the same document that fails Compile when served at
// compile time silently accepts every instance when served only at validation
// time.
func TestValidateUnresolvableFragmentRefInLateFetchedDocument(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc      string
		instance string
		valid    bool
	}{
		"broken anchor ref is reported": {
			doc:      `{"$id": "https://example.test/late.json", "properties": {"x": {"$ref": "#nope"}}}`,
			instance: `{"x": 1}`,
		},
		"broken pointer ref is reported": {
			doc:      `{"$id": "https://example.test/late.json", "properties": {"x": {"$ref": "#/missing/here"}}}`,
			instance: `{"x": 1}`,
		},
		"resolvable fragment ref keeps validating": {
			doc: `{"$id": "https://example.test/late.json",` +
				` "$defs": {"n": {"type": "integer"}}, "properties": {"x": {"$ref": "#/$defs/n"}}}`,
			instance: `{"x": 1}`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "https://example.test/late.json"}`))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := &lateResolver{doc: doc}

			v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err, "a resolver miss at compile time is tolerated")

			resolver.armed.Store(true)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err,
				"an unresolvable fragment ref in a late-fetched document must not silently pass")
			require.ErrorContains(t, err, "cannot resolve $ref",
				"the fragment spelling must report like the absolute spelling")
		})
	}
}

// TestCompileToleratesFragmentRefToLateDocument locks in the Remote References
// contract for a remote $ref carrying a fragment: a document unresolvable at
// compile time is tolerated exactly like the fragment-less spelling, the
// validation walk reports the miss as a "cannot resolve $ref" error, and the
// ref validates normally once the resolver serves the document. Without the
// compile-time reference walk's document-miss tolerance the validator could
// never be built.
func TestCompileToleratesFragmentRefToLateDocument(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		ref string
		doc string
	}{
		"pointer fragment": {
			ref: "https://example.test/late.json#/$defs/x",
			doc: `{"$id": "https://example.test/late.json", "$defs": {"x": {"type": "integer"}}}`,
		},
		"anchor fragment": {
			ref: "https://example.test/late.json#a",
			doc: `{"$id": "https://example.test/late.json",` +
				` "$defs": {"x": {"$anchor": "a", "type": "integer"}}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(`{"properties": {"p": {"$ref": "` + tc.ref + `"}}}`))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err, "a fragment ref to an unresolvable document compiles without a resolver")

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := &lateResolver{doc: doc}

			v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err, "a resolver miss at compile time is tolerated for a fragment ref")

			err = v.ValidateJSON(t.Context(), []byte(`{"p": 1}`))
			require.ErrorContains(t, err, "cannot resolve $ref",
				"an unserved document reports through the validation walk")

			resolver.armed.Store(true)

			require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"p": 1}`)),
				"the fragment resolves once the resolver serves the document")
			require.ErrorContains(t, v.ValidateJSON(t.Context(), []byte(`{"p": "s"}`)),
				`expected "integer"`, "the served document's constraint applies")
		})
	}
}

// TestValidateUnresolvableFragmentRefInFallbackTarget covers the fallback
// variant: a broken fragment ref two levels behind JSON-pointer fallback
// targets. The compile-time reference walk resolves fallback-borne refs
// tolerantly, so Compile tolerates the miss and the validation walk must
// report it instead of silently accepting the instance.
func TestValidateUnresolvableFragmentRefInFallbackTarget(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(
		`{"x-custom": {"sub": {"$ref": "#/x-custom/other"},` +
			` "other": {"properties": {"p": {"$ref": "#/nope/missing"}}}}, "$ref": "#/x-custom/sub"}`,
	))
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), schema)
	require.NoError(t, err, "the own-reference gate is one level deep by design")

	err = v.ValidateJSON(t.Context(), []byte(`{"p": 1}`))
	require.Error(t, err,
		"an unresolvable fragment ref in a fallback target must not silently pass")
	require.ErrorContains(t, err, `cannot resolve $ref "#/nope/missing"`)
}

// lateResolver answers [jsonschema.ErrNotResolved] until armed, then serves its
// fixed document: the shape of a resolver whose documents become available only
// after compilation, which Compile explicitly tolerates.
type lateResolver struct {
	doc   *jsonschema.Schema
	armed atomic.Bool
}

// ResolveRef serves the fixed document once armed, and a miss before that.
func (r *lateResolver) ResolveRef(_ context.Context, _ string) (*jsonschema.Schema, error) {
	if !r.armed.Load() {
		return nil, jsonschema.ErrNotResolved
	}

	return r.doc, nil
}

// TestValidateLateFetchedRemoteStructuralChecks locks in that a remote document
// first fetched at validation time is vetted with the same structural checks
// Compile applies to compile-time-fetched remotes. Without them a late-fetched
// document with a negative bound rejects every instance, and one with a
// Draft-07 items array under a 2020-12 run silently accepts every element.
func TestValidateLateFetchedRemoteStructuralChecks(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   string
		doc      string
		instance string
		err      error
		valid    bool
	}{
		"negative bound in late-fetched document": {
			schema:   `{"$ref": "https://example.test/late.json"}`,
			doc:      `{"maxItems": -1}`,
			instance: `[]`,
			err:      jsonschema.ErrNegativeBound,
		},
		"items array in late-fetched document under 2020-12": {
			schema:   `{"$ref": "https://example.test/late.json"}`,
			doc:      `{"items": [{"type": "string"}]}`,
			instance: `[123]`,
			err:      jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"invalid type name in late-fetched document": {
			schema:   `{"$ref": "https://example.test/late.json"}`,
			doc:      `{"type": "strng"}`,
			instance: `"hello"`,
			err:      jsonschema.ErrInvalidType,
		},
		"items array in late-fetched document under draft-07 keeps tuple semantics": {
			schema:   `{"$schema": "http://json-schema.org/draft-07/schema#", "$ref": "https://example.test/late.json"}`,
			doc:      `{"items": [{"type": "string"}]}`,
			instance: `["ok"]`,
			valid:    true,
		},
		"well-formed late-fetched document validates": {
			schema:   `{"$ref": "https://example.test/late.json"}`,
			doc:      `{"type": "string"}`,
			instance: `"hello"`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := &lateResolver{doc: doc}

			v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err, "a resolver miss at compile time is tolerated")

			resolver.armed.Store(true)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.ErrorIs(t, err, jsonschema.ErrRefResolve,
				"a structurally invalid late-fetched document must fail the ref loudly")
			require.ErrorIs(t, err, tc.err,
				"the structural sentinel must stay reachable through the validation error")
		})
	}
}

// TestValidateLateFetchedRemoteIDCollision pins that a document first fetched
// during a validation run is held to the identifier rule Compile applies to a
// compile-time fetch. The late document claims the root's own URI, so the two
// documents leave every reference naming it ambiguous. Compile tolerates the
// miss, and the run that reaches the reference reports the collision rather
// than letting the late document take the root's URI for the rest of the run.
func TestValidateLateFetchedRemoteIDCollision(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://example.test/root.json", "$ref": "https://example.test/late.json"}`,
	))
	require.NoError(t, err)

	// The document carries both faults, so the assertions below pin which one
	// the run names.
	doc, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://example.test/root.json", "type": "strnig"}`,
	))
	require.NoError(t, err)

	resolver := &lateResolver{doc: doc}

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err, "a resolver miss at compile time is tolerated")

	resolver.armed.Store(true)

	err = v.ValidateJSON(t.Context(), []byte(`"hello"`))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision,
		"the late document claims the URI the root holds")
	require.ErrorIs(t, err, jsonschema.ErrRefResolve,
		"the collision surfaces through the referencing ref")
	require.NotErrorIs(t, err, jsonschema.ErrInvalidType,
		"the collision is settled ahead of the structural vet, which the misspelled type would fail")
}

// TestCompileRemoteTransitiveBrokenRef locks in that the compile-time
// reference walk vets references transitively: a broken local fragment ref
// two or more ref-hops inside a compile-time-fetched remote document must
// fail Compile like its purely-local and one-hop siblings, instead of
// compiling into a validator whose known-document silent skip then accepts
// every instance.
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

// TestCompileTransitiveFallbackTargetVetted locks in that a JSON-pointer
// fallback target first materialized while vetting a remote's registered refs
// (a fragment ref two remote hops from the root) is structurally vetted at
// Compile. The identical shape one hop deep fails Compile. Without that vet
// the two-hop shape compiles cleanly and then fails every Validate run with a
// ref-resolve error instead.
func TestCompileTransitiveFallbackTargetVetted(t *testing.T) {
	t.Parallel()

	inner, err := jsonschema.ParseSchema([]byte(`{
		"$ref": "#/examples/0",
		"examples": [{"type": "strnig"}]
	}`))
	require.NoError(t, err)

	outer, err := jsonschema.ParseSchema([]byte(`{"$ref": "https://example.test/b.json"}`))
	require.NoError(t, err)

	resolver := mapResolver{
		"https://example.test/a.json": outer,
		"https://example.test/b.json": inner,
	}

	for name, ref := range map[string]string{
		"one hop":  `{"$ref": "https://example.test/b.json"}`,
		"two hops": `{"$ref": "https://example.test/a.json"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root, err := jsonschema.ParseSchema([]byte(ref))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), root, jsonschema.WithRefResolver(resolver))
			require.ErrorIs(t, err, jsonschema.ErrInvalidType,
				"the fallback target's invalid type name must fail Compile at any ref depth")
		})
	}
}

// TestValidateURNEncodedRef exercises registration/lookup symmetry for a
// percent-encoded relative $id under an opaque URN base: the embedded resource
// registers under the same key the absolute $ref spells, so the ref resolves
// and validation reports the leaf type failure rather than an unresolved ref.
func TestValidateURNEncodedRef(t *testing.T) {
	t.Parallel()

	doc := stringtest.Input(`
		{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id": "urn:example:root",
			"$defs": {"s": {"$id": "sub%2Fx", "type": "string"}},
			"properties": {"x": {"$ref": "urn:example:sub%2Fx"}}
		}
	`)

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(doc), &schema))

	compiled, err := jsonschema.Compile(t.Context(), &schema)
	require.NoError(t, err)

	err = compiled.Validate(t.Context(), map[string]any{"x": 5})
	require.Error(t, err)

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)
	assert.NotContains(t, verr.Error(), "cannot resolve")
	assert.Contains(t, verr.Error(), `expected "string"`)
}

// TestValidateURNDotSegmentRef exercises RFC 3986 5.2.2 for an opaque URN
// base: a dot-segmented relative $id registers under the canonical
// dot-segment-free key, so the absolute $ref spelling resolves to it the same
// way it does under a hierarchical base.
func TestValidateURNDotSegmentRef(t *testing.T) {
	t.Parallel()

	doc := stringtest.Input(`
		{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id": "urn:example:a/b/c",
			"$defs": {"d": {"$id": "../d", "type": "integer"}},
			"$ref": "urn:example:a/d"
		}
	`)

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(doc), &schema))

	compiled, err := jsonschema.Compile(t.Context(), &schema)
	require.NoError(t, err)

	require.NoError(t, compiled.Validate(t.Context(), 5))

	err = compiled.Validate(t.Context(), "not an integer")
	require.Error(t, err)

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)
	assert.NotContains(t, verr.Error(), "cannot resolve")
	assert.Contains(t, verr.Error(), `expected "integer"`)
}

// TestValidateURNRootedRef exercises RFC 3986 5.2.2 for a rooted reference
// path against an opaque URN base: a path beginning with "/" replaces the base
// path outright rather than merging with it, so the $ref targets the key the
// absolute $id spelling registers under (urn:/c, not urn:example:a//c).
func TestValidateURNRootedRef(t *testing.T) {
	t.Parallel()

	doc := stringtest.Input(`
		{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id": "urn:example:a/b",
			"$defs": {"A": {"$id": "urn:/c", "type": "integer"}},
			"$ref": "/c"
		}
	`)

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(doc), &schema))

	compiled, err := jsonschema.Compile(t.Context(), &schema)
	require.NoError(t, err)

	require.NoError(t, compiled.Validate(t.Context(), 5))

	err = compiled.Validate(t.Context(), "not an integer")
	require.Error(t, err)

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)
	assert.NotContains(t, verr.Error(), "cannot resolve")
	assert.Contains(t, verr.Error(), `expected "integer"`)
}
