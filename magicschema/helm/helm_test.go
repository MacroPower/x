package helm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/magicschema/helm"
)

func TestDefaultNamesReturnsFreshSlice(t *testing.T) {
	t.Parallel()

	first := helm.DefaultNames()
	first[0] = "mutated"

	assert.NotEqual(t, first, helm.DefaultNames(),
		"mutating one result must not affect later calls")
}

func TestDefaultRegistryMatchesDefaultNames(t *testing.T) {
	t.Parallel()

	registry := helm.DefaultRegistry()

	for _, name := range helm.DefaultNames() {
		assert.Contains(t, registry, name)
		assert.Equal(t, name, registry[name].Name())
	}

	assert.Len(t, registry, len(helm.DefaultNames()))
}
