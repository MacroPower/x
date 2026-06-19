package goast_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/goast"
)

// parseFiles parses src as a single Go source file with comments attached and
// returns it wrapped in the []*ast.File slice the goast helpers consume.
func parseFiles(t *testing.T, src string) []*ast.File {
	t.Helper()

	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	require.NoError(t, err)

	return []*ast.File{f}
}

func TestBaseTypeName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name string
		want string
	}{
		"generic":          {name: "Box[int]", want: "Box"},
		"generic two args": {name: "Pair[int, string]", want: "Pair"},
		"non-generic":      {name: "Plain", want: "Plain"},
		"empty":            {name: "", want: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, goast.BaseTypeName(tc.name))
		})
	}
}

func TestTypeDoc(t *testing.T) {
	t.Parallel()

	const src = `package p

// Spec documents the type spec.
type Spec struct{}

// Group documents the only spec in the group.
type (
	Solo int
)

type (
	// Inner documents a spec in a multi-spec group.
	Inner int
	Other int
)

type Bare struct{}
`

	files := parseFiles(t, src)

	tests := map[string]struct {
		typeName string
		want     string
		found    bool
	}{
		"typespec doc":        {typeName: "Spec", want: "Spec documents the type spec.", found: true},
		"single-spec gendecl": {typeName: "Solo", want: "Group documents the only spec in the group.", found: true},
		"multi-spec typespec": {typeName: "Inner", want: "Inner documents a spec in a multi-spec group.", found: true},
		"multi-spec no doc":   {typeName: "Other", want: "", found: true},
		"found without doc":   {typeName: "Bare", want: "", found: true},
		"absent type":         {typeName: "Missing", want: "", found: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc, found := goast.TypeDoc(files, tc.typeName)
			require.Equal(t, tc.found, found)
			require.Equal(t, tc.want, doc)
		})
	}
}

func TestFindTypeSpec(t *testing.T) {
	t.Parallel()

	const src = `package p

type Present struct{}
`

	files := parseFiles(t, src)

	tests := map[string]struct {
		typeName string
		found    bool
	}{
		"present": {typeName: "Present", found: true},
		"absent":  {typeName: "Absent", found: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ts := goast.FindTypeSpec(files, tc.typeName)
			if !tc.found {
				require.Nil(t, ts)

				return
			}

			require.NotNil(t, ts)
			require.Equal(t, tc.typeName, ts.Name.Name)
		})
	}
}

func TestStructFieldDoc(t *testing.T) {
	t.Parallel()

	const src = `package p

// Embedded carries a doc comment on its embedded field.
type Embedded struct {
	// Named documents the named field.
	Named int

	Undocumented int

	// Base documents the embedded field.
	Base
}

type Base struct{}
`

	files := parseFiles(t, src)
	ts := goast.FindTypeSpec(files, "Embedded")
	require.NotNil(t, ts)

	st, ok := ts.Type.(*ast.StructType)
	require.True(t, ok)

	tests := map[string]struct {
		field string
		want  string
		found bool
	}{
		"named field":    {field: "Named", want: "Named documents the named field.", found: true},
		"embedded field": {field: "Base", want: "Base documents the embedded field.", found: true},
		"undocumented":   {field: "Undocumented", want: "", found: false},
		"absent":         {field: "Missing", want: "", found: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc, found := goast.StructFieldDoc(st, tc.field)
			require.Equal(t, tc.found, found)
			require.Equal(t, tc.want, doc)
		})
	}
}

func TestEmbeddedFieldName(t *testing.T) {
	t.Parallel()

	const src = `package p

type Host struct {
	Ident
	*Star
	pkg.Selector
	*pkg.Box[T]
	Index[T]
	IndexList[T, U]
	Unrecognized func()
}
`

	files := parseFiles(t, src)
	ts := goast.FindTypeSpec(files, "Host")
	require.NotNil(t, ts)

	st, ok := ts.Type.(*ast.StructType)
	require.True(t, ok)

	// Map the embedded field expressions onto the names we expect goast to
	// derive from them, in declaration order. The final field is a named field
	// whose function type is an unrecognized embedded shape, so it yields "".
	want := []string{"Ident", "Star", "Selector", "Box", "Index", "IndexList", ""}
	require.Len(t, st.Fields.List, len(want))

	for i, field := range st.Fields.List {
		require.Equal(t, want[i], goast.EmbeddedFieldName(field.Type))
	}
}
