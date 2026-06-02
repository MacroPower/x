// Package main implements fixture-based tests for the goreleaser toolchain
// module. The pure tag/digest helpers have fast unit tests in the module's
// release subpackage; these exercise the engine-backed Check against a
// synthetic fixture.
package main

import (
	"context"
	"fmt"
	"slices"

	"dagger/tests/internal/dagger"
)

// Tests exercises the goreleaser toolchain.
type Tests struct{}

// fixture returns the synthetic project with a valid .goreleaser.yaml.
func (t *Tests) fixture() *dagger.Directory {
	return dag.CurrentModule().Source().Directory("testdata/fixture")
}

// All runs every test in sequence and reports the first failure.
func (t *Tests) All(ctx context.Context) error {
	cases := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"check-valid", t.CheckValid},
		{"check-invalid", t.CheckInvalid},
		{"version-tags", t.VersionTags},
	}
	for _, tc := range cases {
		if err := tc.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	return nil
}

// fixtureRemote is a fake origin URL; goreleaser check needs a remote
// configured to resolve version/ref templates (it does not fetch it).
const fixtureRemote = "https://github.com/example/fixture.git"

// CheckValid verifies a valid .goreleaser.yaml passes `goreleaser check`.
func (t *Tests) CheckValid(ctx context.Context) error {
	return dag.Goreleaser(dagger.GoreleaserOpts{
		Source:    t.fixture(),
		RemoteURL: fixtureRemote,
	}).Check(ctx)
}

// CheckInvalid verifies a malformed .goreleaser.yaml fails the check. The
// remote is configured so the only reason for failure is the bad config.
func (t *Tests) CheckInvalid(ctx context.Context) error {
	bad := dag.Directory().WithNewFile(".goreleaser.yaml", "version: 2\nbuilds: not-a-list\n")
	err := dag.Goreleaser(dagger.GoreleaserOpts{
		Source:    bad,
		RemoteURL: fixtureRemote,
	}).Check(ctx)
	if err == nil {
		return fmt.Errorf("expected goreleaser check to reject an invalid config")
	}
	return nil
}

// VersionTags exercises the pure tag helper through the module API.
func (t *Tests) VersionTags(ctx context.Context) error {
	got, err := dag.Goreleaser(dagger.GoreleaserOpts{Source: t.fixture()}).VersionTags(ctx, "v1.2.3")
	if err != nil {
		return err
	}
	want := []string{"latest", "v1.2.3", "v1", "v1.2"}
	if !slices.Equal(got, want) {
		return fmt.Errorf("VersionTags = %v, want %v", got, want)
	}
	return nil
}
