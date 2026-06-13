package jsonschema

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"
)

// GoCommentProvider is the AST-backed [DescriptionProvider]: it extracts Go doc
// comments from source files by loading and parsing package sources with
// [golang.org/x/tools/go/packages] at generation time, so it requires access
// to source files; when sources cannot be located for a type, it silently
// supplies no comment, so a binary deployed without sources generates
// schemas without descriptions rather than failing. A canceled or expired
// context is the exception: it is reported as an error, aborting
// generation, since package loading is the cancellable work the Generate
// context exists for. Construct it with [NewGoCommentProvider] and register
// it with [WithDescriptionProvider]. Wrapping it composes other sources with AST
// extraction: overrides for specific types, or a pre-extracted map
// consulted first.
//
// Parsed packages are cached on the provider, keyed by import path, so a
// provider shared across Generate calls loads each package once. The cache
// mutex makes the provider safe for concurrent use.
type GoCommentProvider struct {
	cache   map[string][]*ast.File
	loadDir string
	mu      sync.Mutex
}

// GoCommentProviderOption configures a [GoCommentProvider] at construction.
// Options are produced by this package's With* constructors; the sealed
// interface form matches the package's other option types ([GenerateOption],
// [ValidateOption], [InlineOption]).
type GoCommentProviderOption interface {
	applyGoCommentProvider(p *GoCommentProvider)
}

// goCommentProviderOptionFunc adapts a function to [GoCommentProviderOption].
type goCommentProviderOptionFunc func(*GoCommentProvider)

func (f goCommentProviderOptionFunc) applyGoCommentProvider(p *GoCommentProvider) { f(p) }

// WithLoadDir returns a [GoCommentProviderOption] setting the directory
// package loading runs in, the way the go tool's -C flag does. Package paths
// resolve against that directory's module; the default is the process
// working directory, which finds nothing when the types' module lives
// elsewhere (a generator invoked from another module, a test binary run from
// a temporary directory).
func WithLoadDir(dir string) GoCommentProviderOption {
	return goCommentProviderOptionFunc(func(p *GoCommentProvider) { p.loadDir = dir })
}

// NewGoCommentProvider returns a [GoCommentProvider] with an empty package
// cache. Nil options are skipped, so an optional option can be passed
// unconditionally.
func NewGoCommentProvider(opts ...GoCommentProviderOption) *GoCommentProvider {
	p := &GoCommentProvider{
		cache: map[string][]*ast.File{},
	}

	for _, opt := range opts {
		if opt != nil {
			opt.applyGoCommentProvider(p)
		}
	}

	return p
}

// baseTypeName strips the type-argument list from a reflect type name so an
// instantiated generic type (whose Name() is e.g. "Box[int]") matches its source
// declaration ("Box"). For a non-generic type it returns the name unchanged.
func baseTypeName(name string) string {
	base, _, _ := strings.Cut(name, "[")

	return base
}

// TypeDescription returns the doc comment for a named type.
//
// Matching is by package path and unqualified type name, since reflection does
// not expose source positions. A non-package-scope type (for example one
// declared inside a function) that shadows a package-level name may therefore
// receive the package-level type's comment.
func (ce *GoCommentProvider) TypeDescription(ctx context.Context, tc TypeContext) (string, error) {
	t := tc.Type
	if t.Name() == "" || t.PkgPath() == "" {
		return "", nil
	}

	name := baseTypeName(t.Name())

	files, err := ce.sourceFiles(ctx, t.PkgPath())
	if err != nil {
		return "", err
	}

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
					return strings.TrimSpace(ts.Doc.Text()), nil
				}

				if gd.Doc != nil && len(gd.Specs) == 1 {
					return strings.TrimSpace(gd.Doc.Text()), nil
				}
			}
		}
	}

	return "", nil
}

// FieldDescription returns the doc comment for a struct field, located via
// the declaring type ([FieldContext.Owner]) and the Go field name
// ([FieldContext.StructField]).
//
// As with TypeDescription, matching is by package path and unqualified type name,
// so a non-package-scope struct that shadows a package-level name may receive
// the package-level struct's field comments.
func (ce *GoCommentProvider) FieldDescription(ctx context.Context, fc FieldContext) (string, error) {
	structType, fieldName := fc.Owner, fc.StructField.Name
	if structType.Name() == "" || structType.PkgPath() == "" {
		return "", nil
	}

	name := baseTypeName(structType.Name())

	files, err := ce.sourceFiles(ctx, structType.PkgPath())
	if err != nil {
		return "", err
	}

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
							return strings.TrimSpace(field.Doc.Text()), nil
						}
					}
				}
			}
		}
	}

	return "", nil
}

// sourceFiles returns parsed AST files for the package at the given import path.
// It uses go/packages for source resolution, which handles module cache and
// standard library packages. Results are cached per package path; a load cut
// short by context cancellation is reported as an error and not cached, so a
// later call under a live context still loads the package.
func (ce *GoCommentProvider) sourceFiles(ctx context.Context, pkgPath string) ([]*ast.File, error) {
	if pkgPath == "" {
		return nil, nil
	}

	ce.mu.Lock()
	defer ce.mu.Unlock()

	if files, ok := ce.cache[pkgPath]; ok {
		return files, nil
	}

	files, err := ce.loadPackage(ctx, pkgPath)
	if err != nil {
		return nil, err
	}

	ce.cache[pkgPath] = files

	return files, nil
}

// loadPackage uses go/packages to load and parse source files for a package.
// A load attempted under a done context reports the context's error;
// every other load failure returns nil files, the silent skip the
// [GoCommentProvider] docs describe.
//
// The configured Mode (NeedName | NeedFiles | NeedSyntax) parses but does not
// type-check, so the only per-file problems that arise are parse errors and
// import resolution failures. Package-level errors (an unrelated sibling file
// with a parse problem, an unresolved import, and so on) do not discard the
// successfully parsed files: go/packages populates Syntax with every AST that
// parsed cleanly while aggregating per-file problems separately in Errors.
// Best-effort comment extraction uses whatever parsed, so a single bad file in
// the package does not drop doc comments for the types that did parse.
func (ce *GoCommentProvider) loadPackage(ctx context.Context, pkgPath string) ([]*ast.File, error) {
	cfg := &packages.Config{
		Context: ctx,
		Dir:     ce.loadDir,
		Mode:    packages.NeedName | packages.NeedFiles | packages.NeedSyntax,
		ParseFile: func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
			return parser.ParseFile(fset, filename, src, parser.ParseComments)
		},
	}

	pkgs, err := packages.Load(cfg, pkgPath)

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return nil, fmt.Errorf("load package %s: %w", pkgPath, ctxErr)
	}

	if err != nil || len(pkgs) == 0 {
		//nolint:nilerr // A live-context load failure is the documented silent skip.
		return nil, nil
	}

	return pkgs[0].Syntax, nil
}
