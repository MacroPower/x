package main

import (
	"context"
	"path/filepath"

	"dagger/go/internal/dagger"
)

// FormatGo runs golangci-lint --fix across all discovered Go modules and
// returns the merged changeset of Go source file changes.
//
// Not annotated +generate: consumers register this toolchain directly and
// typically compose FormatGo into their own +generate Format alongside other
// formatters (e.g. prettier); annotating it here would run golangci-lint
// --fix twice and merge overlapping changesets under dagger generate.
func (m *Go) FormatGo(
	ctx context.Context,
	// Include only modules whose directory matches one of these globs.
	// +optional
	include []string,
	// Exclude modules whose directory matches any of these globs.
	// +optional
	exclude []string,
) (*dagger.Changeset, error) {
	return m.eachModuleChangeset(ctx, include, exclude, "format-go",
		func(_ context.Context, mod string) (*dagger.Changeset, error) {
			return m.FormatGoModule(mod), nil
		})
}

// FormatGoModule runs golangci-lint --fix on a single module directory
// and returns the changeset.
func (m *Go) FormatGoModule(
	// Module directory relative to the source root.
	mod string,
) *dagger.Changeset {
	outDir := "/src"
	if isNestedModule(mod) {
		outDir = filepath.Join("/src", mod)
	}
	goFmt := m.LintBase(mod).
		WithExec([]string{"golangci-lint", "run", "--fix"}).
		Directory(outDir)
	if isNestedModule(mod) {
		// Re-assemble into a full-source changeset so callers get
		// paths relative to the source root.
		full := m.Source.WithDirectory(mod, goFmt)
		return full.Changes(m.Source)
	}
	return goFmt.Changes(m.Source)
}

// Generate runs go generate and returns the changeset of generated files
// against the original source.
//
// +generate
func (m *Go) Generate(
	ctx context.Context,
	// Packages to run go generate against.
	// +optional
	// +default=["./..."]
	pkgs []string,
) (*dagger.Changeset, error) {
	pkgs, err := m.resolvePkgs(ctx, pkgs)
	if err != nil {
		return nil, err
	}
	generated := m.Env("").
		WithExec(append([]string{"go", "generate"}, pkgs...)).
		Directory("/src").
		WithoutDirectory(".git")
	return generated.Changes(m.Source), nil
}
