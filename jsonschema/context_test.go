package jsonschema_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// ctxMarkerKey keys a marker value placed on contexts handed to the context
// entry points, so a recording resolver can verify it received the same
// context the caller supplied.
type ctxMarkerKey struct{}

// remoteIntegerSchema returns a schema whose root is a remote $ref, so every
// compile or validation run must consult the configured resolver.
func remoteIntegerSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Ref:    "https://example.com/integer.json",
	}
}

// remoteIntegerPropertySchema returns a schema with a property whose only
// keyword is a remote $ref, so inlining replaces that node wholesale and the
// fetched type is observable at Properties["count"].
func remoteIntegerPropertySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema",
		Type:   "object",
		Properties: map[string]*jsonschema.Schema{
			"count": {Ref: "https://example.com/integer.json"},
		},
	}
}

// recordingResolver implements [jsonschema.RefResolver], recording every
// context received by ResolveRef. While disabled it misses (returns nil,
// nil), so a schema can be compiled without caching the remote target and
// the validation-time resolution path is exercised. A canceled context
// propagates its error, mimicking a resolver that fetches over the network.
type recordingResolver struct {
	schemas  map[string]*jsonschema.Schema
	mu       sync.Mutex
	ctxs     []context.Context
	disabled bool
}

func (r *recordingResolver) ResolveRef(ctx context.Context, uri string) (*jsonschema.Schema, error) {
	r.recordCtx(ctx)

	err := ctx.Err()
	if err != nil {
		//nolint:wrapcheck // A real resolver surfaces ctx.Err() as-is.
		return nil, err
	}

	return r.lookup(uri)
}

// recordCtx appends ctx to the received-context log.
func (r *recordingResolver) recordCtx(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.ctxs = append(r.ctxs, ctx)
}

func (r *recordingResolver) lookup(uri string) (*jsonschema.Schema, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.disabled {
		//nolint:nilnil // A miss returns (nil, nil): the validator treats it as "unresolved, skip".
		return nil, nil
	}

	if s, ok := r.schemas[uri]; ok {
		return s, nil
	}

	//nolint:nilnil // A miss returns (nil, nil): the validator treats it as "unresolved, skip".
	return nil, nil
}

// setDisabled flips whether lookups miss, so a test can hide the remote
// schema from Compile and reveal it for validation.
func (r *recordingResolver) setDisabled(disabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.disabled = disabled
}

// recordedCtxs returns a copy of the contexts ResolveRef received.
func (r *recordingResolver) recordedCtxs() []context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]context.Context(nil), r.ctxs...)
}

// TestCompileContextPassesContextToResolver pins the compile-time resolution
// path: refs resolved while compiling reach the resolver carrying the
// context given to CompileContext.
func TestCompileContextPassesContextToResolver(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(t.Context(), ctxMarkerKey{}, "compile")
	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
	}

	v, err := jsonschema.CompileContext(ctx, remoteIntegerSchema(),
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	ctxs := resolver.recordedCtxs()
	require.NotEmpty(t, ctxs, "compile-time resolution should call ResolveRef")

	for _, got := range ctxs {
		assert.Equal(t, "compile", got.Value(ctxMarkerKey{}))
	}

	// The compile-time result is cached, so validation works without the
	// resolver being consulted again.
	require.NoError(t, v.ValidateContext(t.Context(), 42.0))
}

// TestValidateContextPassesContextToResolver pins the validation-time
// resolution path: a remote ref the compile could not resolve is fetched
// during the run under the context given to ValidateContext, not the compile
// context and not a context captured at configuration time.
func TestValidateContextPassesContextToResolver(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
		disabled: true,
	}

	// The resolver misses during Compile, so the remote target is not cached
	// and each validation run must resolve it itself.
	v, err := jsonschema.CompileContext(
		context.WithValue(t.Context(), ctxMarkerKey{}, "compile"),
		remoteIntegerSchema(),
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	resolver.setDisabled(false)

	ctx := context.WithValue(t.Context(), ctxMarkerKey{}, "validate")
	require.NoError(t, v.ValidateContext(ctx, 42.0))
	require.Error(t, v.ValidateContext(ctx, "not an integer"))

	ctxs := resolver.recordedCtxs()
	require.NotEmpty(t, ctxs)

	var validateCalls int

	for _, got := range ctxs {
		if got.Value(ctxMarkerKey{}) == "validate" {
			validateCalls++
		}
	}

	assert.NotZero(t, validateCalls,
		"validation-time resolution should carry the ValidateContext context")
}

// TestValidateJSONContextPassesContextToResolver covers the byte-decoding
// context entry point delegating into the same per-run context plumbing.
func TestValidateJSONContextPassesContextToResolver(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
		disabled: true,
	}

	v, err := jsonschema.Compile(remoteIntegerSchema(), jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	resolver.setDisabled(false)

	ctx := context.WithValue(t.Context(), ctxMarkerKey{}, "validate-json")
	require.NoError(t, v.ValidateJSONContext(ctx, []byte(`42`)))

	ctxs := resolver.recordedCtxs()
	require.NotEmpty(t, ctxs)
	assert.Equal(t, "validate-json", ctxs[len(ctxs)-1].Value(ctxMarkerKey{}))
}

// TestValidateContextCancellation pins that a canceled context surfaces from
// the resolver as a validation error wrapping both ErrRefResolve and the
// context's own error, and that the failure is not cached: a later run with a
// live context succeeds.
func TestValidateContextCancellation(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
		disabled: true,
	}

	v, err := jsonschema.Compile(remoteIntegerSchema(), jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	resolver.setDisabled(false)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	err = v.ValidateContext(canceled, 42.0)
	require.Error(t, err)
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, context.Canceled)

	// The canceled run must not poison the validator: a live context resolves
	// and validates cleanly.
	require.NoError(t, v.ValidateContext(t.Context(), 42.0))
}

// TestPackageLevelContextHelpers covers the one-shot ValidateContext and
// ValidateJSONContext variants end to end with a context-aware resolver.
func TestPackageLevelContextHelpers(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
	}

	ctx := context.WithValue(t.Context(), ctxMarkerKey{}, "one-shot")

	err := jsonschema.ValidateContext(ctx, remoteIntegerSchema(), 42.0,
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	err = jsonschema.ValidateJSONContext(ctx, remoteIntegerSchema(), []byte(`"nope"`),
		jsonschema.WithRefResolver(resolver),
	)
	require.Error(t, err)

	ctxs := resolver.recordedCtxs()
	require.NotEmpty(t, ctxs)

	for _, got := range ctxs {
		assert.Equal(t, "one-shot", got.Value(ctxMarkerKey{}))
	}
}

// TestCompileJSONContextPassesContextToResolver pins that the schema-document
// entry point forwards its context to compile-time resolution.
func TestCompileJSONContextPassesContextToResolver(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
	}

	ctx := context.WithValue(t.Context(), ctxMarkerKey{}, "compile-json")

	v, err := jsonschema.CompileJSONContext(ctx,
		[]byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"https://example.com/integer.json"}`),
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)
	require.NoError(t, v.Validate(42.0))

	ctxs := resolver.recordedCtxs()
	require.NotEmpty(t, ctxs)

	for _, got := range ctxs {
		assert.Equal(t, "compile-json", got.Value(ctxMarkerKey{}))
	}
}

// TestContextlessEntryPointsPassBackground pins the documented default: the
// context-less entry points hand the resolver context.Background, not nil.
func TestContextlessEntryPointsPassBackground(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
	}

	err := jsonschema.Validate(remoteIntegerSchema(), 42.0,
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	ctxs := resolver.recordedCtxs()
	require.NotEmpty(t, ctxs)

	for _, got := range ctxs {
		//nolint:usetesting // The assertion is about the documented Background default.
		assert.Equal(t, context.Background(), got)
	}
}

// TestInlineContextPassesContextToResolver pins the inlining resolution path:
// document fetches reach the resolver carrying the context given to
// InlineContext.
func TestInlineContextPassesContextToResolver(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(t.Context(), ctxMarkerKey{}, "inline")
	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
	}

	inlined, err := jsonschema.InlineContext(ctx, remoteIntegerPropertySchema(),
		jsonschema.WithInlineResolver(resolver),
	)
	require.NoError(t, err)
	assert.Equal(t, "integer", inlined.Properties["count"].Type)

	ctxs := resolver.recordedCtxs()
	require.NotEmpty(t, ctxs, "document fetches should call ResolveRef")

	for _, got := range ctxs {
		assert.Equal(t, "inline", got.Value(ctxMarkerKey{}))
	}
}

// TestInlineContextCancellation pins that a canceled context surfaces from
// the resolver as an error wrapping both ErrRefResolve and the context's own
// error.
func TestInlineContextCancellation(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := jsonschema.InlineContext(canceled, remoteIntegerPropertySchema(),
		jsonschema.WithInlineResolver(resolver),
	)
	require.Error(t, err)
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, context.Canceled)
}

// TestInlinePassesBackgroundToContextResolver pins the documented default:
// the context-less Inline hands the resolver context.Background, not nil.
func TestInlinePassesBackgroundToContextResolver(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{
		schemas: map[string]*jsonschema.Schema{
			"https://example.com/integer.json": {Type: "integer"},
		},
	}

	inlined, err := jsonschema.Inline(remoteIntegerPropertySchema(),
		jsonschema.WithInlineResolver(resolver),
	)
	require.NoError(t, err)
	assert.Equal(t, "integer", inlined.Properties["count"].Type)

	ctxs := resolver.recordedCtxs()
	require.NotEmpty(t, ctxs)

	for _, got := range ctxs {
		//nolint:usetesting // The assertion is about the documented Background default.
		assert.Equal(t, context.Background(), got)
	}
}

// TestResolverThroughContextEntryPoints pins that a minimal resolver works
// through every context entry point.
func TestResolverThroughContextEntryPoints(t *testing.T) {
	t.Parallel()

	resolver := mapResolver{
		"https://example.com/integer.json": {Type: "integer"},
	}

	v, err := jsonschema.CompileContext(t.Context(), remoteIntegerSchema(),
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	require.NoError(t, v.ValidateContext(t.Context(), 42.0))
	require.Error(t, v.ValidateContext(t.Context(), "not an integer"))
	require.NoError(t, v.ValidateJSONContext(t.Context(), []byte(`42`)))

	err = jsonschema.ValidateContext(t.Context(), remoteIntegerSchema(), 42.0,
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	err = jsonschema.ValidateJSONContext(t.Context(), remoteIntegerSchema(), []byte(`42`),
		jsonschema.WithRefResolver(resolver),
	)
	require.NoError(t, err)

	inlined, err := jsonschema.InlineContext(t.Context(), remoteIntegerPropertySchema(),
		jsonschema.WithInlineResolver(resolver),
	)
	require.NoError(t, err)
	assert.Equal(t, "integer", inlined.Properties["count"].Type)
}
