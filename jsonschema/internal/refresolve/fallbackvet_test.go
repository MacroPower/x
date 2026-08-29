package refresolve_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/refresolve"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
)

// errVetRefused is the sentinel the rejecting vet below answers with, standing
// in for the structural sentinels the parent package's vets carry.
var errVetRefused = errors.New("refused")

// pointerTargetDeps returns the Deps the JSON-pointer fallback needs: the
// sub-schema traversal every registry walk takes, plus a Materialize spelled as
// a plain JSON round-trip, which is what the parent's ParseSchemaValue does for
// the shapes these tests use.
func pointerTargetDeps() refresolve.Deps {
	return refresolve.Deps{
		Children: schemafield.Children,
		Materialize: func(node any) (*jsonschema.Schema, error) {
			data, err := json.Marshal(node)
			if err != nil {
				return nil, err
			}

			target := &jsonschema.Schema{}

			err = json.Unmarshal(data, target)
			if err != nil {
				return nil, err
			}

			return target, nil
		},
	}
}

// TestResolveRefMarksVetRejectedTargets pins Result.TargetRejected against the
// one outcome it must separate from a plain pointer miss. Both leave Target
// nil, and the closure walk answers them oppositely. A rejection is settled,
// so a strict walk fails on it; a miss inside a present document is the
// outcome a tolerant pass defers. Deriving the flag from a nil Target rather
// than from the vet's answer passes every test in the parent package and
// silently makes every pointer miss terminal, which is what this pins.
func TestResolveRefMarksVetRejectedTargets(t *testing.T) {
	t.Parallel()

	const rootURI = "https://example.test/root.json"

	tests := map[string]struct {
		ref      string
		reject   bool
		resolved bool
		rejected bool
		err      error
	}{
		"the vet rejects a materialized target": {
			ref:      "#/x-custom/sub",
			reject:   true,
			rejected: true,
			err:      errVetRefused,
		},
		"the vet accepts a materialized target": {
			ref:      "#/x-custom/sub",
			resolved: true,
		},
		"the pointer locates nothing": {
			ref: "#/x-custom/absent",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := &jsonschema.Schema{
				ID:    rootURI,
				Type:  "object",
				Extra: map[string]any{"x-custom": map[string]any{"sub": map[string]any{"type": "string"}}},
			}

			reg := refresolve.NewRegistry(pointerTargetDeps(), refresolve.Draft2020, false)
			reg.Build(root, rootURI)

			vetter := schemavet.NewVetter(schemavet.Profile{})

			sess := reg.NewSession(func(sc *jsonschema.Schema, locator string) (schemavet.Node, error) {
				if tc.reject {
					return schemavet.Node{}, errVetRefused
				}

				return vetter.Vet(sc, locator)
			})

			res := sess.ResolveRef(root, tc.ref, nil)

			if tc.resolved {
				require.NotNil(t, res.Target, "the vet accepted the target, so the resolution reports it")
			} else {
				require.Nil(t, res.Target)
			}

			assert.Equal(t, tc.rejected, res.TargetRejected,
				"only a vet refusal marks the result rejected")
			require.ErrorIs(t, res.Err, tc.err)
		})
	}
}

// TestFallbackTargetsExcludeRejected pins that a rejected target registers
// nothing and joins no frontier, so the closure walk never ref-walks past a
// target the vet refused.
func TestFallbackTargetsExcludeRejected(t *testing.T) {
	t.Parallel()

	const rootURI = "https://example.test/root.json"

	root := &jsonschema.Schema{
		ID:    rootURI,
		Type:  "object",
		Extra: map[string]any{"x-custom": map[string]any{"sub": map[string]any{"type": "string"}}},
	}

	reg := refresolve.NewRegistry(pointerTargetDeps(), refresolve.Draft2020, false)
	reg.Build(root, rootURI)

	sess := reg.NewSession(func(_ *jsonschema.Schema, _ string) (schemavet.Node, error) {
		return schemavet.Node{}, errVetRefused
	})

	require.True(t, sess.ResolveRef(root, "#/x-custom/sub", nil).TargetRejected)
	assert.Empty(t, sess.FallbackTargets(), "a refused target joins no frontier")
}
