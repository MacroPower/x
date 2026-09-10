// Package goast loads, parses, and reads doc comments and type/field shapes out
// of a Go package (go/ast). It loads package sources at generation time, locates
// a named type, walks its fields (following same-package type aliases), and
// unwraps the type expressions Go uses to name embedded fields, so the
// generation half's comment provider can serve the doc comment that belongs to a
// reflected type or struct field.
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
// reporting whether the type was found. The doc comment sits on the TypeSpec
// itself or, for a spec with none, on the enclosing GenDecl, whether that
// declaration holds one spec or a parenthesized group; go/doc attaches a
// group's comment to every spec in it lacking its own, and TypeDoc reads it
// the same way. A type found without a doc comment reports an empty string
// and true; an absent type reports an empty string and false.
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

				// The doc comment sits on the TypeSpec itself or, for a spec
				// with none, on the GenDecl, whether the declaration holds one
				// spec or a parenthesized group; go/doc attaches the group's
				// comment to every spec lacking its own. The type name is
				// unique per package, so return as soon as it matches instead
				// of scanning the rest.
				if ts.Doc != nil {
					return strings.TrimSpace(ts.Doc.Text()), true
				}

				if gd.Doc != nil {
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
// in st, reporting whether a documented match was found. A field's doc
// comment is the comment group above it, or, for a field with none, the line
// comment after it on the same line, the two forms gopls presents as a
// field's documentation. A field carrying both keeps the group above it.
func StructFieldDoc(st *ast.StructType, fieldName string) (string, bool) {
	for _, field := range st.Fields.List {
		// An embedded field has no name idents; Go names it after the embedded
		// type, and a doc comment hangs off the field itself.
		if len(field.Names) == 0 {
			if EmbeddedFieldName(field.Type) == fieldName {
				return fieldDoc(field)
			}

			continue
		}

		for _, ident := range field.Names {
			if ident.Name == fieldName {
				return fieldDoc(field)
			}
		}
	}

	return "", false
}

// fieldDoc returns the trimmed text of the comment group above field, or of
// the line comment after it when there is none, reporting whether field
// carries either.
func fieldDoc(field *ast.Field) (string, bool) {
	switch {
	case field.Doc != nil:
		return strings.TrimSpace(field.Doc.Text()), true
	case field.Comment != nil:
		return strings.TrimSpace(field.Comment.Text()), true
	default:
		return "", false
	}
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

// StructFieldDocThroughAliases returns the doc comment for fieldName on the
// struct named typeName, following a chain of same-package named types
// (type Foo Bar, or a generic instantiation type Foo Bar[int]) down to the
// underlying struct, so a field comment declared on Bar is found when the field
// is reported under Foo. The type-argument list on typeName is stripped first
// ([BaseTypeName]). It reports ok=false when no reachable type is a struct (a
// cross-package alias or non-struct underlying type carries no locally
// scannable fields) or the type is not found. A visited set guards against a
// malformed cyclic alias chain.
func StructFieldDocThroughAliases(files []*ast.File, typeName, fieldName string) (string, bool) {
	name := BaseTypeName(typeName)
	seen := map[string]bool{}

	for !seen[name] {
		seen[name] = true

		ts := FindTypeSpec(files, name)
		if ts == nil {
			return "", false
		}

		switch underlying := ts.Type.(type) {
		case *ast.StructType:
			return StructFieldDoc(underlying, fieldName)

		case *ast.Ident:
			// A same-package named type (type Foo Bar); follow to Bar.
			name = underlying.Name

		case *ast.IndexExpr:
			// A same-package generic instantiation (type Foo Bar[int]); follow
			// to the base type Bar. A non-Ident base (a cross-package
			// instantiation pkg.Bar[int]) carries no locally scannable fields.
			id, ok := underlying.X.(*ast.Ident)
			if !ok {
				return "", false
			}

			name = id.Name

		case *ast.IndexListExpr:
			// As *ast.IndexExpr, but with several type arguments
			// (type Foo Bar[int, string]).
			id, ok := underlying.X.(*ast.Ident)
			if !ok {
				return "", false
			}

			name = id.Name

		default:
			// A cross-package alias (an *ast.SelectorExpr) or a non-struct
			// underlying type carries no locally scannable struct fields.
			return "", false
		}
	}

	return "", false
}
