package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/alpha"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/beta"
)

func TestGenerateFor_TypeOverrideDropsOrphanedDefUnderCollision(t *testing.T) {
	t.Parallel()

	// Field A registers alpha.Widget's def first, then its only reference is
	// detached by the type= override. Field B's reference to beta.Widget carries
	// the provisional "#/$defs/Widget" token until render; reachability must not
	// resolve that token to the earlier-registered alpha.Widget def, which would
	// retain it as an unreferenced $defs entry.
	type Root struct {
		A alpha.Widget `json:"a" jsonschema:"type=integer"`
		B beta.Widget  `json:"b"`
	}

	s, err := jsonschema.GenerateFor[Root](t.Context())
	require.NoError(t, err)

	assert.Equal(t, "integer", s.Properties["a"].Type)
	require.Equal(t, "#/$defs/beta_Widget", s.Properties["b"].Ref)

	require.Contains(t, s.Defs, "beta_Widget")
	assert.NotContains(t, s.Defs, "alpha_Widget",
		"a def orphaned by a type= override must be dropped even when its base name collides with a live def")
}

// TestGenerateFor_RootInliningUnderBaseNameCollision pins that the
// reachability walk never string-resolves a ref node's provisional token.
// Each field ref's payload is aliased into its parent's Properties, so the
// scan can reach it through the parent before the node walk claims it; the
// provisional "#/$defs/Knot" on B's ref would then resolve to the
// first-registered claimant -- the orphaned alpha.Knot def, whose body's
// back-reference to KnotRoot would falsely report the root's def as
// referenced elsewhere and suppress root inlining.
func TestGenerateFor_RootInliningUnderBaseNameCollision(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.KnotRoot](t.Context())
	require.NoError(t, err)

	assert.Empty(t, s.Ref,
		"the root's def is referenced from nowhere once alpha.Knot is orphaned, so the root must inline")
	assert.Equal(t, "object", s.Type)
	require.Contains(t, s.Properties, "b")
	assert.Equal(t, "#/$defs/beta_Knot", s.Properties["b"].Ref)

	require.Contains(t, s.Defs, "beta_Knot")
	assert.NotContains(t, s.Defs, "alpha_Knot",
		"the orphaned colliding def must be dropped")
	assert.NotContains(t, s.Defs, "KnotRoot",
		"an inlined root leaves no def behind")
}
