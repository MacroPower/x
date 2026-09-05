package goast

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"

	"golang.org/x/tools/go/packages"
)

// LoadPackageFiles uses go/packages to load and parse the source files for the
// package at pkgPath, resolving paths against dir (empty means the process
// working directory). The returned bool reports whether the load reached a
// definitive result worth caching. A load attempted under a done context
// reports the context's error (and false); any other load failure returns no
// files and false (the documented silent skip), so a transient failure is
// retried rather than cached. A successful load returns its parsed files and
// true, even when the package legitimately has no source files.
//
// The configured Mode (NeedName | NeedFiles | NeedSyntax) parses but does not
// type-check, so the only per-file problems that arise are parse errors and
// import resolution failures. Package-level errors (an unrelated sibling file
// with a parse problem, an unresolved import, and so on) do not discard the
// successfully parsed files: go/packages populates Syntax with every AST that
// parsed cleanly while aggregating per-file problems separately in Errors.
// Best-effort comment extraction uses whatever parsed, so a single bad file in
// the package does not drop doc comments for the types that did parse.
func LoadPackageFiles(ctx context.Context, dir, pkgPath string) ([]*ast.File, bool, error) {
	cfg := &packages.Config{
		Context: ctx,
		Dir:     dir,
		Mode:    packages.NeedName | packages.NeedFiles | packages.NeedSyntax,
		ParseFile: func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
			return parser.ParseFile(fset, filename, src, parser.ParseComments)
		},
	}

	// A type declared in package main reflects the import path "main", which
	// no pattern resolves; its sources are the package in the load directory
	// when that directory is a main package.
	pattern := pkgPath
	if pkgPath == mainPkgPath {
		pattern = "."
	}

	pkgs, err := packages.Load(cfg, pattern)

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return nil, false, fmt.Errorf("load package %s: %w", pkgPath, ctxErr)
	}

	if err != nil || len(pkgs) == 0 {
		//nolint:nilerr // A live-context load failure is the documented silent skip; loaded=false keeps it out of the cache so a later call retries.
		return nil, false, nil
	}

	pkg := pkgs[0]

	// Most load failures (an unresolvable pattern, a module the proxy could
	// not serve, a failed go list) arrive as package errors with a nil error.
	// Nothing parsed and something failed, so the result is not definitive
	// and stays out of the cache for a later call to retry.
	if len(pkg.Syntax) == 0 && len(pkg.GoFiles) == 0 && len(pkg.Errors) > 0 {
		return nil, false, nil
	}

	// A load directory that is not a main package holds no sources for a
	// main-package type, and no retry changes that.
	if pkgPath == mainPkgPath && pkg.Name != mainPkgPath {
		return nil, true, nil
	}

	return pkg.Syntax, true, nil
}

// mainPkgPath is the import path reflection reports for a type declared in
// package main.
const mainPkgPath = "main"
