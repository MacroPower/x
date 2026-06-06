package main

import (
	"context"
	"fmt"
	"path"

	"golang.org/x/mod/modfile"
)

// defaultPkgs is the package pattern functions accept by default. At the
// root of a go.work workspace it matches nothing (the root is not itself a
// module), so [Go.resolvePkgs] expands it into per-module patterns there.
const defaultPkgs = "./..."

// resolvePkgs expands the default "./..." package pattern for workspace
// repositories. When the source root has a go.work but no go.mod, "./..."
// matches nothing, so each workspace module listed in a use directive
// becomes its own "./<dir>/..." pattern. Explicit patterns and
// single-module sources pass through unchanged.
func (m *Go) resolvePkgs(ctx context.Context, pkgs []string) ([]string, error) {
	expand := false
	for _, pkg := range pkgs {
		if pkg == defaultPkgs {
			expand = true
			break
		}
	}
	if !expand {
		return pkgs, nil
	}

	modules, err := m.workspaceModules(ctx)
	if err != nil {
		return nil, err
	}
	if modules == nil {
		return pkgs, nil
	}

	var resolved []string
	for _, pkg := range pkgs {
		if pkg != defaultPkgs {
			resolved = append(resolved, pkg)
			continue
		}
		for _, dir := range modules {
			resolved = append(resolved, "./"+dir+"/...")
		}
	}
	return resolved, nil
}

// workspaceModules returns the module directories named by the source's
// go.work use directives, or nil when the source is not a workspace root
// (it has a root go.mod, or no go.work at all).
func (m *Go) workspaceModules(ctx context.Context) ([]string, error) {
	rootMods, err := m.Source.Glob(ctx, "go.mod")
	if err != nil {
		return nil, fmt.Errorf("glob root go.mod: %w", err)
	}
	if len(rootMods) > 0 {
		return nil, nil
	}

	works, err := m.Source.Glob(ctx, "go.work")
	if err != nil {
		return nil, fmt.Errorf("glob go.work: %w", err)
	}
	if len(works) == 0 {
		return nil, nil
	}

	contents, err := m.Source.File("go.work").Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("read go.work: %w", err)
	}
	work, err := modfile.ParseWork("go.work", []byte(contents), nil)
	if err != nil {
		return nil, fmt.Errorf("parse go.work: %w", err)
	}

	dirs := make([]string, 0, len(work.Use))
	for _, use := range work.Use {
		dirs = append(dirs, path.Clean(use.Path))
	}
	return dirs, nil
}
