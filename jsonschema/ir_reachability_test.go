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
