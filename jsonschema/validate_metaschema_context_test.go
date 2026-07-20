package jsonschema_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestCompileMetaschemaResolverContextNormalized locks in that the metaschema
// resolver receives a normalized context: a nil context passed to Compile (a
// misuse Go tolerates and runContext exists to defend against) must reach the
// resolver as a usable non-nil context, matching every other resolver
// invocation, so a well-behaved resolver that reads the context does not
// panic.
func TestCompileMetaschemaResolverContextNormalized(t *testing.T) {
	t.Parallel()

	var (
		called    bool
		ctxWasNil bool
	)

	resolver := jsonschema.RefResolverFunc(func(ctx context.Context, _ string) (*jsonschema.Schema, error) {
		called = true
		ctxWasNil = ctx == nil
		// A nil context panics here the way any deadline-aware resolver would.
		_, _ = ctx.Deadline()

		return nil, jsonschema.ErrNotResolved
	})

	var nilCtx context.Context

	schema := &jsonschema.Schema{Schema: "https://example.test/meta"}

	_, err := jsonschema.Compile(nilCtx, schema, jsonschema.WithMetaSchemaResolver(resolver))
	require.NoError(t, err, "a metaschema miss falls through to the default vocabularies")
	require.True(t, called, "the resolver must have been consulted")
	require.False(t, ctxWasNil,
		"a nil compile context must be normalized before reaching the metaschema resolver")
}
