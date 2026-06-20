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

	pkgs, err := packages.Load(cfg, pkgPath)

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return nil, false, fmt.Errorf("load package %s: %w", pkgPath, ctxErr)
	}

	if err != nil || len(pkgs) == 0 {
		//nolint:nilerr // A live-context load failure is the documented silent skip; loaded=false keeps it out of the cache so a later call retries.
		return nil, false, nil
	}

	return pkgs[0].Syntax, true, nil
}
