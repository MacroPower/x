package jsonopts //nolint:testpackage // In-package by design: the classification table is guarded from inside its own package (see jsonschema/CLAUDE.md); the no-in-package-test policy is main-package only.

import (
	"encoding/json/v2"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonv1 "encoding/json"
)

// The toolchain packages that export Options constructors, as import paths
// under GOROOT/src.
var optionPackages = []string{"encoding/json", "encoding/json/v2", "encoding/json/jsontext"}

// toolchainConstructors parses the option packages under build.Default.GOROOT
// and returns "importpath.Name" for every exported top-level func whose sole
// result is the Options identifier. Parsing rather than importing sidesteps
// the goexperiment.jsonv2 build tag, and skips a constructor that exists only
// as commented-out source. A missing GOROOT/src fails the caller rather than
// skipping it, since a skip would defeat the guard.
func toolchainConstructors(t *testing.T) []string {
	t.Helper()

	var found []string

	for _, pkg := range optionPackages {
		dir := filepath.Join(build.Default.GOROOT, "src", pkg)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err, "the guard needs the toolchain source under GOROOT/src")

		fset := token.NewFileSet()

		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}

			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
			require.NoError(t, err)

			for _, decl := range file.Decls {
				if ctor, ok := constructorName(decl); ok {
					found = append(found, pkg+"."+ctor)
				}
			}
		}
	}

	slices.Sort(found)

	return found
}

// constructorName returns the name of decl when it is an exported top-level
// func with no receiver whose sole result is the Options identifier.
func constructorName(decl ast.Decl) (string, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv != nil || !fn.Name.IsExported() ||
		fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return "", false
	}

	result, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
	if !ok || result.Name != "Options" {
		return "", false
	}

	return fn.Name.Name, true
}

// TestTableCoversToolchain pins the table to the constructor set the
// toolchain exports, in both directions, so a new constructor has to be
// classified before a toolchain bump passes.
func TestTableCoversToolchain(t *testing.T) {
	t.Parallel()

	got := make([]string, 0, len(Table))
	for _, row := range Table {
		got = append(got, row.Key)
	}

	slices.Sort(got)
	assert.Equal(t, toolchainConstructors(t), got, "classify every Options constructor the toolchain exports")
}

// TestTableProbes pins that every row but a combinator or a bundle carries a
// probe, and that a combinator carries none.
func TestTableProbes(t *testing.T) {
	t.Parallel()

	for _, row := range Table {
		switch {
		case row.Class == ClassCombinator, row.Key == "encoding/json.DefaultOptionsV1":
			assert.Nil(t, row.Set, "%s sets nothing of its own", row.Key)
		default:
			assert.NotNil(t, row.Set, "%s needs a probe", row.Key)
		}
	}
}

func TestRefused(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		opts json.Options
		want string
		ok   bool
	}{
		"nothing set":      {opts: json.JoinOptions()},
		"honored only":     {opts: json.FormatNilSliceAsNull(true)},
		"ignored only":     {opts: json.Deterministic(true)},
		"stringify":        {opts: json.StringifyNumbers(true), want: "StringifyNumbers", ok: true},
		"stringify unset":  {opts: json.JoinOptions(json.StringifyNumbers(true), json.StringifyNumbers(false))},
		"v1 bundle":        {opts: jsonv1.DefaultOptionsV1(), want: "OmitEmptyWithLegacySemantics", ok: true},
		"duration as nano": {opts: jsonv1.FormatDurationAsNano(true), want: "FormatDurationAsNano", ok: true},
		"marshalers": {
			opts: json.WithMarshalers(json.MarshalFunc(func(time.Time) ([]byte, error) { return []byte(`""`), nil })),
			want: "WithMarshalers",
			ok:   true,
		},
		"boolean outranks marshalers": {
			opts: json.JoinOptions(
				json.WithMarshalers(json.MarshalFunc(func(time.Time) ([]byte, error) { return []byte(`""`), nil })),
				jsonv1.FormatDurationAsNano(true),
			),
			want: "FormatDurationAsNano", ok: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := Refused(tc.opts)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHonored(t *testing.T) {
	t.Parallel()

	assert.Equal(t, Flags{}, Honored(json.JoinOptions()))
	assert.Equal(t,
		Flags{NilSliceNull: true, NilMapNull: true, OmitZeroFields: true},
		Honored(json.JoinOptions(
			json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true), json.OmitZeroStructFields(true),
		)),
	)
	assert.Equal(
		t,
		Flags{NilMapNull: true},
		Honored(
			json.JoinOptions(
				json.FormatNilSliceAsNull(true),
				json.FormatNilSliceAsNull(false),
				json.FormatNilMapAsNull(true),
			),
		),
		"a later option unsets an earlier one",
	)
}

func TestRowName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "StringifyNumbers", Row{Key: "encoding/json/v2.StringifyNumbers"}.Name())
	assert.Equal(t, "WithIndent", Row{Key: "encoding/json/jsontext.WithIndent"}.Name())
}
