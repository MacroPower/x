// Package helm provides a convenience function for registering the built-in
// Helm annotation parsers with a [magicschema.Registry].
package helm

import (
	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm/bitnami"
	"go.jacobcolvin.com/x/magicschema/helm/dadav"
	"go.jacobcolvin.com/x/magicschema/helm/losisin"
	"go.jacobcolvin.com/x/magicschema/helm/norwoodj"
)

// DefaultRegistry returns a [magicschema.Registry] populated with the four
// built-in Helm annotators: helm-schema (dadav), helm-values-schema (losisin),
// bitnami, and helm-docs (norwoodj).
func DefaultRegistry() magicschema.Registry {
	r := make(magicschema.Registry)
	r.Add(dadav.New(), losisin.New(), bitnami.New(), norwoodj.New())

	return r
}
