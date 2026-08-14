package jsonschema_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// duplicateAnchorResolver serves one remote document whose two $defs entries
// both claim the same $anchor, the malformed shape where duplicate-key
// precedence becomes observable.
type duplicateAnchorResolver struct{}

func (duplicateAnchorResolver) ResolveRef(_ context.Context, uri string) (*jsonschema.Schema, error) {
	if uri != "http://example.com/doc" {
		return nil, fmt.Errorf("unexpected uri %q", uri)
	}

	//nolint:wrapcheck // The test fails on any parse error either way.
	return jsonschema.ParseSchema([]byte(stringtest.Input(`
		{
			"$defs": {
				"one": {"$anchor": "a", "type": "string"},
				"two": {"$anchor": "a", "type": "number"}
			}
		}
	`)))
}

// TestInlineDuplicateAnchorMatchesValidator pins that the validator and the
// inliner resolve a duplicate $anchor in a fetched document to the same
// target (the first walked, matching the validator's only-if-absent
// fetched-document precedence). The two engines share the refresolve core so
// they cannot disagree on well-formed input; a malformed duplicate-key
// document must not reopen the gap, or validating an instance against the
// original schema and against its Inline output gives opposite verdicts.
func TestInlineDuplicateAnchorMatchesValidator(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(stringtest.Input(`
		{
			"properties": {
				"x": {"$ref": "http://example.com/doc#a"}
			}
		}
	`)))
	require.NoError(t, err)

	direct, err := jsonschema.Compile(t.Context(), root,
		jsonschema.WithRefResolver(duplicateAnchorResolver{}))
	require.NoError(t, err)

	inlined, err := jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(duplicateAnchorResolver{}))
	require.NoError(t, err)

	fromInline, err := jsonschema.Compile(t.Context(), inlined)
	require.NoError(t, err)

	for _, instance := range []string{`{"x": "s"}`, `{"x": 1.5}`} {
		directErr := direct.ValidateJSON(t.Context(), []byte(instance))
		inlineErr := fromInline.ValidateJSON(t.Context(), []byte(instance))

		require.Equal(t, directErr == nil, inlineErr == nil,
			"engines disagree on %s: direct=%v inline=%v", instance, directErr, inlineErr)
	}
}
