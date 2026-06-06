package jsonschema

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"
)

// commentExtractor extracts Go doc comments from source files.
//
// Generation drives the extractor from a single goroutine, so the mutex
// guarding the cache is a forward-looking guard that keeps the extractor safe
// should generation ever fan out across goroutines.
type commentExtractor struct {
	cache map[string][]*ast.File
	mu    sync.Mutex
}

// newCommentExtractor returns a new commentExtractor with an empty cache.
func newCommentExtractor() *commentExtractor {
	return &commentExtractor{
		cache: map[string][]*ast.File{},
	}
}

// baseTypeName strips the type-argument list from a reflect type name so an
// instantiated generic type (whose Name() is e.g. "Box[int]") matches its source
// declaration ("Box"). For a non-generic type it returns the name unchanged.
func baseTypeName(name string) string {
	base, _, _ := strings.Cut(name, "[")

	return base
}

// typeComment returns the doc comment for a named type.
//
// Matching is by package path and unqualified type name, since reflection does
// not expose source positions. A non-package-scope type (for example one
// declared inside a function) that shadows a package-level name may therefore
// receive the package-level type's comment.
func (ce *commentExtractor) typeComment(t reflect.Type) string {
	if t.Name() == "" || t.PkgPath() == "" {
		return ""
	}

	name := baseTypeName(t.Name())

	files := ce.sourceFiles(t.PkgPath())
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}

			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != name {
					continue
				}

				// Doc comment can be on the GenDecl (for single-spec decls)
				// or on the TypeSpec itself.
				if ts.Doc != nil {
					return strings.TrimSpace(ts.Doc.Text())
				}

				if gd.Doc != nil && len(gd.Specs) == 1 {
					return strings.TrimSpace(gd.Doc.Text())
				}
			}
		}
	}

	return ""
}

// fieldComment returns the doc comment for a struct field.
//
// As with typeComment, matching is by package path and unqualified type name, so
// a non-package-scope struct that shadows a package-level name may receive the
// package-level struct's field comments.
func (ce *commentExtractor) fieldComment(structType reflect.Type, fieldName string) string {
	if structType.Name() == "" || structType.PkgPath() == "" {
		return ""
	}

	name := baseTypeName(structType.Name())

	files := ce.sourceFiles(structType.PkgPath())
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}

			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != name {
					continue
				}

				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}

				for _, field := range st.Fields.List {
					for _, ident := range field.Names {
						if ident.Name == fieldName && field.Doc != nil {
							return strings.TrimSpace(field.Doc.Text())
						}
					}
				}
			}
		}
	}

	return ""
}

// sourceFiles returns parsed AST files for the package at the given import path.
// It uses go/packages for source resolution, which handles module cache and
// standard library packages. Results are cached per package path.
func (ce *commentExtractor) sourceFiles(pkgPath string) []*ast.File {
	if pkgPath == "" {
		return nil
	}

	ce.mu.Lock()
	defer ce.mu.Unlock()

	if files, ok := ce.cache[pkgPath]; ok {
		return files
	}

	files := ce.loadPackage(pkgPath)
	ce.cache[pkgPath] = files

	return files
}

// loadPackage uses go/packages to load and parse source files for a package.
// Returns nil if the package cannot be loaded.
//
// The configured Mode (NeedName | NeedFiles | NeedSyntax) parses but does not
// type-check, so the only per-file problems that arise are parse errors and
// import resolution failures. Package-level errors (an unrelated sibling file
// with a parse problem, an unresolved import, and so on) do not discard the
// successfully parsed files: go/packages populates Syntax with every AST that
// parsed cleanly while aggregating per-file problems separately in Errors.
// Best-effort comment extraction uses whatever parsed, so a single bad file in
// the package does not drop doc comments for the types that did parse.
func (ce *commentExtractor) loadPackage(pkgPath string) []*ast.File {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax,
		ParseFile: func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
			return parser.ParseFile(fset, filename, src, parser.ParseComments)
		},
	}

	pkgs, err := packages.Load(cfg, pkgPath)
	if err != nil || len(pkgs) == 0 {
		return nil
	}

	return pkgs[0].Syntax
}
