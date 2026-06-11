package helm_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/magicschema/helm"
)

func TestDefaultNames(t *testing.T) {
	t.Parallel()

	// The --annotators flag default in magicschema's config.go must stay a
	// literal: helm imports magicschema, so the reverse import would cycle.
	// Pinning the joined names to that literal catches drift between the
	// two declarations.
	const flagDefault = "helm-schema,helm-values-schema,bitnami,helm-docs"

	assert.Equal(t, flagDefault, strings.Join(helm.DefaultNames(), ","))
}

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
