// Package main implements tests for the cosign toolchain module.
//
// Real sign/verify is exercised by the consumer release pipelines, which run
// against real registries with real OIDC/key credentials -- cosign cannot be
// signed-and-verified in isolation here because that needs a reachable registry
// and credentials, and the module deliberately does not bind a service. These
// tests cover the callable wiring and the no-op (empty digests) guard.
package main

import (
	"context"
	"fmt"
)

// Tests exercises the cosign toolchain.
type Tests struct{}

// All runs every test in sequence and reports the first failure.
func (t *Tests) All(ctx context.Context) error {
	cases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"no-op-on-empty", t.NoOpOnEmpty},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

// NoOpOnEmpty verifies both sign functions short-circuit to nil when there are
// no digests to sign (so a release with nothing to sign does not invoke cosign
// or require credentials), confirming the functions are wired and callable.
func (t *Tests) NoOpOnEmpty(ctx context.Context) error {
	secret := dag.SetSecret("cosign-test", "unused")

	if err := dag.Cosign().SignKeyless(ctx, []string{}, "", secret); err != nil {
		return fmt.Errorf("SignKeyless([]) = %w, want nil", err)
	}
	if err := dag.Cosign().SignWithKey(ctx, []string{}, secret); err != nil {
		return fmt.Errorf("SignWithKey([]) = %w, want nil", err)
	}
	return nil
}
