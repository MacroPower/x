// Package goast reads doc comments and type/field shapes out of a parsed Go
// package (go/ast). The generation half's comment provider loads package
// sources at generation time and these helpers locate a named type, walk its
// fields, and unwrap the type expressions Go uses to name embedded fields, so
// the provider can serve the doc comment that belongs to a reflected type or
// struct field.
package goast

import (
	"go/ast"
	"go/token"
	"strings"
)

// BaseTypeName strips the type-argument list from a reflect type name so an
// instantiated generic type (whose Name() is e.g. "Box[int]") matches its source
// declaration ("Box"). For a non-generic type it returns the name unchanged.
func BaseTypeName(name string) string {
	base, _, _ := strings.Cut(name, "[")

	return base
}

// TypeDoc returns the doc comment for the type named name declared in files,
// reporting whether the type was found. The doc comment can be on the TypeSpec
// itself or, for a single-spec declaration, on the enclosing GenDecl. A type
// found without a doc comment reports an empty string and true; an absent type
// reports an empty string and false.
func TypeDoc(files []*ast.File, name string) (string, bool) {
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

				// Doc comment can be on the GenDecl (for single-spec decls) or
				// on the TypeSpec itself. The type name is unique per package, so
				// return as soon as it matches instead of scanning the rest.
				if ts.Doc != nil {
					return strings.TrimSpace(ts.Doc.Text()), true
				}

				if gd.Doc != nil && len(gd.Specs) == 1 {
					return strings.TrimSpace(gd.Doc.Text()), true
				}

				return "", true
			}
		}
	}

	return "", false
}

// FindTypeSpec returns the type declaration named name in files, or nil. A type
// name is unique per package, so the first match is authoritative.
func FindTypeSpec(files []*ast.File, name string) *ast.TypeSpec {
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}

			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if ok && ts.Name.Name == name {
					return ts
				}
			}
		}
	}

	return nil
}

// StructFieldDoc returns the trimmed doc comment for the field named fieldName
// in st, reporting whether a documented match was found.
func StructFieldDoc(st *ast.StructType, fieldName string) (string, bool) {
	for _, field := range st.Fields.List {
		// An embedded field has no name idents; Go names it after the embedded
		// type, and a doc comment hangs off the field itself.
		if len(field.Names) == 0 {
			if field.Doc != nil && EmbeddedFieldName(field.Type) == fieldName {
				return strings.TrimSpace(field.Doc.Text()), true
			}

			continue
		}

		for _, ident := range field.Names {
			if ident.Name == fieldName && field.Doc != nil {
				return strings.TrimSpace(field.Doc.Text()), true
			}
		}
	}

	return "", false
}

// EmbeddedFieldName returns the field name Go assigns to an embedded
// (anonymous) struct field, which is the unqualified name of the embedded type.
// It unwraps a leading pointer, a package qualifier, and a generic
// instantiation (Box[T] or Box[T, U]) down to the base type name; an
// unrecognized shape yields "", leaving the field undescribed rather than
// mismatched.
func EmbeddedFieldName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}

	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return EmbeddedFieldName(t.X)
	case *ast.IndexListExpr:
		return EmbeddedFieldName(t.X)
	default:
		return ""
	}
}
