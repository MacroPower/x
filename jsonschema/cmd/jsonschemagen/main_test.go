package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderMainGo(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cfg        config
		importPath string
		want       string
	}{
		"defaults": {
			cfg: config{
				TypeName: "Config",
				Draft:    "2020",
				Indent:   "  ",
			},
			importPath: "example.com/myapp",
			want: `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"

	target "example.com/myapp"
)

func main() {
	t := reflect.TypeFor[target.Config]()
	opts := []jsonschema.GenerateOption{
	}
	schema, err := jsonschema.Generate(context.Background(), t, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}
`,
		},
		"draft7": {
			cfg: config{
				TypeName: "Settings",
				Draft:    "7",
				Indent:   "\t",
			},
			importPath: "example.com/pkg",
			want: `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"

	target "example.com/pkg"
)

func main() {
	t := reflect.TypeFor[target.Settings]()
	opts := []jsonschema.GenerateOption{
		jsonschema.WithDraft(jsonschema.Draft7),
	}
	schema, err := jsonschema.Generate(context.Background(), t, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(schema, "", "\t")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}
`,
		},
		"all options": {
			cfg: config{
				TypeName:             "MyType",
				Draft:                "2020",
				Comments:             true,
				AdditionalProperties: true,
				Validate:             true,
				Indent:               "    ",
			},
			importPath: "example.com/full",
			want: `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"

	target "example.com/full"
)

func main() {
	t := reflect.TypeFor[target.MyType]()
	opts := []jsonschema.GenerateOption{
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
		jsonschema.WithAdditionalProperties(true),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	}
	schema, err := jsonschema.Generate(context.Background(), t, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(schema, "", "    ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}
`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			err := renderMainGo(&buf, tc.cfg, tc.importPath)
			require.NoError(t, err)
			assert.Equal(t, tc.want, buf.String())
		})
	}
}

func TestRenderGoMod(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		modPath       string
		modDir        string
		jsonschemaDir string
		want          string
	}{
		"different module": {
			modPath:       "example.com/myapp",
			modDir:        "/home/user/myapp",
			jsonschemaDir: "/home/user/go/pkg/mod/go.jacobcolvin.com/x/jsonschema@v0.1.0",
			want: fmt.Sprintf(`module _jsonschemagen_tmp

go %s

require (
	example.com/myapp v0.0.0
	go.jacobcolvin.com/x/jsonschema v0.0.0
)

replace example.com/myapp => /home/user/myapp
replace go.jacobcolvin.com/x/jsonschema => /home/user/go/pkg/mod/go.jacobcolvin.com/x/jsonschema@v0.1.0
`, goDirectiveVersion()),
		},
		"jsonschema module itself": {
			modPath:       "go.jacobcolvin.com/x/jsonschema",
			modDir:        "/home/user/jsonschema",
			jsonschemaDir: "/home/user/jsonschema",
			want: fmt.Sprintf(`module _jsonschemagen_tmp

go %s

require (
	go.jacobcolvin.com/x/jsonschema v0.0.0
)

replace go.jacobcolvin.com/x/jsonschema => /home/user/jsonschema
`, goDirectiveVersion()),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := renderGoMod(tc.modPath, tc.modDir, tc.jsonschemaDir)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRenderGoModQuotesSpecialPaths(t *testing.T) {
	t.Parallel()

	// A double-quote, backtick, or space (all legal in POSIX filenames) must be
	// quoted in the replace directive, or the go.mod lexer rejects the unquoted
	// token with "invalid quoted string".
	tests := map[string]string{
		"double quote":    `/home/user/we"ird/proj`,
		"backtick":        "/home/user/we`ird/proj",
		"space":           "/home/user/we ird/proj",
		"tab":             "/home/user/we\tird/proj",
		"newline":         "/home/user/we\nird/proj",
		"carriage return": "/home/user/we\rird/proj",
	}

	for name, dir := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := renderGoMod("example.com/app", dir, "/home/user/jsonschema")
			want := "replace example.com/app => " + strconv.Quote(dir) + "\n"
			assert.Contains(t, got, want,
				"a path with a special character must be quoted in the replace directive")
		})
	}
}

func TestMergeGoSumKeysOnModuleVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	writeSum := func(name, content string) string {
		t.Helper()

		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))

		return p
	}

	// The two files disagree on the module-zip checksum for the same
	// module@version. The merge omits that key entirely (so go mod tidy
	// re-resolves it) while preserving the distinct go.mod line, the unrelated
	// module, and dropping an exact duplicate.
	first := writeSum("a.sum",
		"example.com/m v1.0.0 h1:AAAA=\n"+
			"example.com/m v1.0.0/go.mod h1:BBBB=\n")
	second := writeSum("b.sum",
		"example.com/m v1.0.0 h1:CONFLICT=\n"+
			"example.com/other v2.0.0 h1:DDDD=\n"+
			"example.com/m v1.0.0/go.mod h1:BBBB=\n")

	got := string(mergeGoSum(first, second))

	want := "example.com/m v1.0.0/go.mod h1:BBBB=\n" +
		"example.com/other v2.0.0 h1:DDDD=\n"

	assert.Equal(t, want, got)
	assert.NotContains(t, got, "CONFLICT",
		"a conflicting checksum must not win")
	assert.NotContains(t, got, "AAAA",
		"a conflicting module-zip checksum is dropped, not kept first")
}

func TestMergeGoSumToleratesCRLF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	writeSum := func(name, content string) string {
		t.Helper()

		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))

		return p
	}

	// A CRLF copy of an entry must deduplicate against its LF twin (not be
	// treated as a conflict) and must be written back without a trailing \r.
	lf := writeSum("lf.sum", "example.com/m v1.0.0 h1:AAAA=\n")
	crlf := writeSum("crlf.sum", "example.com/m v1.0.0 h1:AAAA=\r\nexample.com/n v2.0.0 h1:BBBB=\r\n")

	got := string(mergeGoSum(lf, crlf))

	want := "example.com/m v1.0.0 h1:AAAA=\n" +
		"example.com/n v2.0.0 h1:BBBB=\n"

	assert.Equal(t, want, got)
	assert.NotContains(t, got, "\r", "CRLF endings must not survive into the merged go.sum")
}

func TestSelectMainModule(t *testing.T) {
	t.Parallel()

	stream := `{"Path":"example.com/a","Dir":"/ws/a","GoMod":"/ws/a/go.mod"}
{"Path":"example.com/b","Dir":"/ws/b","GoMod":"/ws/b/go.mod"}`

	t.Run("matches the current module in a workspace", func(t *testing.T) {
		t.Parallel()

		path, dir, err := selectMainModule([]byte(stream), "/ws/b/go.mod")
		require.NoError(t, err)
		assert.Equal(t, "example.com/b", path)
		assert.Equal(t, "/ws/b", dir)
	})

	t.Run("falls back to the first object outside a module", func(t *testing.T) {
		t.Parallel()

		path, dir, err := selectMainModule([]byte(stream), "")
		require.NoError(t, err)
		assert.Equal(t, "example.com/a", path, "an empty GOMOD means outside a module")
		assert.Equal(t, "/ws/a", dir)
	})

	t.Run("errors when no object matches the current module", func(t *testing.T) {
		t.Parallel()

		// Inside a module, no match means the current module is absent from the
		// stream; falling back to an arbitrary module would target the wrong
		// source tree, so this is an error rather than a silent first-object pick.
		_, _, err := selectMainModule([]byte(stream), "/ws/missing/go.mod")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "/ws/missing/go.mod")
	})

	t.Run("errors on an empty stream", func(t *testing.T) {
		t.Parallel()

		_, _, err := selectMainModule(nil, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no module reported")
	})
}

func TestRun_MissingType(t *testing.T) {
	t.Parallel()

	err := run(config{Draft: "2020"}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-type flag is required")
}

func TestRun_InvalidDraft(t *testing.T) {
	t.Parallel()

	err := run(config{TypeName: "Foo", Draft: "4"}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported draft")
}

func TestRun_InvalidIndent(t *testing.T) {
	t.Parallel()

	err := run(config{TypeName: "Foo", Draft: "2020", Indent: "xx"}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid -indent")
}

// buildBinary builds the jsonschemagen binary and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "jsonschemagen")

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join(moduleDir(t), "cmd", "jsonschemagen")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	return binary
}

// moduleDir returns the directory of the jsonschema module.
func moduleDir(t *testing.T) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "go", "list", "-m", "-json", "go.jacobcolvin.com/x/jsonschema")
	out, err := cmd.Output()
	require.NoError(t, err)

	var info moduleInfo

	require.NoError(t, json.Unmarshal(out, &info))

	return info.Dir
}

// createTestModule creates a temporary Go module with the given type definition
// and returns the module directory.
func createTestModule(t *testing.T, typeDef string) string {
	t.Helper()

	dir := t.TempDir()
	jsDir := moduleDir(t)

	goMod := `module example.com/testmod

go ` + goDirectiveVersion() + `

require go.jacobcolvin.com/x/jsonschema v0.0.0

replace go.jacobcolvin.com/x/jsonschema => ` + jsDir + `
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "types.go"), []byte(typeDef), 0o644))

	// Copy go.sum from the jsonschema module so transitive deps resolve.
	data, err := os.ReadFile(filepath.Join(jsDir, "go.sum"))
	if err == nil {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.sum"), data, 0o644))
	}

	return dir
}

func TestIntegration_BasicStruct(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Config struct {
	Name string `+"`"+`json:"name"`+"`"+`
	Port int    `+"`"+`json:"port"`+"`"+`
}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"port": {"type": "integer"}
		},
		"required": ["name", "port"],
		"additionalProperties": false
	}`, string(out))
}

func TestIntegration_Draft7(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Settings struct {
	Debug bool `+"`"+`json:"debug"`+"`"+`
}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Settings", "-draft", "7")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"properties": {
			"debug": {"type": "boolean"}
		},
		"required": ["debug"],
		"additionalProperties": false
	}`, string(out))
}

func TestIntegration_Validate(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type User struct {
	Name  string `+"`"+`json:"name" validate:"required,min=1,max=50"`+"`"+`
	Email string `+"`"+`json:"email" validate:"required,email"`+"`"+`
}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "User", "-validate")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"name": {"type": "string", "minLength": 1, "maxLength": 50},
			"email": {"type": "string", "minLength": 1, "format": "email"}
		},
		"required": ["name", "email"],
		"additionalProperties": false
	}`, string(out))
}

func TestIntegration_Comments(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

// Server holds server configuration.
type Server struct {
	// Host is the server hostname.
	Host string `+"`"+`json:"host"`+"`"+`
	// Port is the server port number.
	Port int `+"`"+`json:"port"`+"`"+`
}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Server", "-comments")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"description": "Server holds server configuration.",
		"properties": {
			"host": {"type": "string", "description": "Host is the server hostname."},
			"port": {"type": "integer", "description": "Port is the server port number."}
		},
		"required": ["host", "port"],
		"additionalProperties": false
	}`, string(out))
}

func TestIntegration_AdditionalProperties(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Loose struct {
	Name string `+"`"+`json:"name"`+"`"+`
}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Loose", "-additional-properties")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		},
		"required": ["name"]
	}`, string(out))
}

func TestIntegration_OutputFile(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Item struct {
	ID string `+"`"+`json:"id"`+"`"+`
}
`)

	outFile := filepath.Join(t.TempDir(), "item.schema.json")
	cmd := exec.CommandContext(t.Context(), binary, "-type", "Item", "-o", outFile)
	cmd.Dir = dir

	cmdOut, err := cmd.CombinedOutput()
	require.NoError(t, err, "output: %s", cmdOut)

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"id": {"type": "string"}
		},
		"required": ["id"],
		"additionalProperties": false
	}`, string(data))
}

func TestIntegration_MissingType(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Exists struct{}
`)

	cmd := exec.CommandContext(t.Context(), binary)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "-type flag is required")
}

func TestIntegration_InvalidType(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Exists struct{}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "DoesNotExist")
	cmd.Dir = dir

	_, err := cmd.Output()
	require.Error(t, err)
}

// cmdStderr extracts stderr from an exec error for test diagnostics.
func cmdStderr(err error) string {
	if err == nil {
		return ""
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return string(exitErr.Stderr)
	}

	return err.Error()
}

func TestRenderMainGoRejectsInjectedTypeName(t *testing.T) {
	t.Parallel()

	// The type name is interpolated into a Go source template, so renderMainGo
	// rejects any value that is not a plain Go identifier rather than emitting
	// source a crafted name could escape into.
	cfg := config{
		TypeName: "Foo]()\n\tos.Exit(0)\n\t//",
		Draft:    "2020",
		Indent:   "  ",
	}

	var b strings.Builder

	err := renderMainGo(&b, cfg, "example.com/myapp")
	require.Error(t, err, "renderMainGo should reject TypeName with special characters")
}

func TestRenderMainGoRejectsUnexportedTypeName(t *testing.T) {
	t.Parallel()

	// An unexported type is inaccessible as target.<name> from the generated
	// package, so renderMainGo rejects it up front with a clear message instead
	// of deferring to an opaque "undefined: target.myConfig" compiler error.
	cfg := config{
		TypeName: "myConfig",
		Draft:    "2020",
		Indent:   "  ",
	}

	var b strings.Builder

	err := renderMainGo(&b, cfg, "example.com/myapp")
	require.Error(t, err, "renderMainGo should reject an unexported TypeName")
	assert.Contains(t, err.Error(), "exported")
}

func TestRenderMainGoRejectsInjectedImportPath(t *testing.T) {
	t.Parallel()

	// The import path is interpolated into the template as target
	// "{{.ImportPath}}", so renderMainGo rejects paths containing the quote,
	// backtick, backslash, or whitespace characters that could break out of the
	// import declaration's string literal.
	cfg := config{
		TypeName: "Foo",
		Draft:    "2020",
		Indent:   "  ",
	}

	malicious := `example.com/myapp"` + "\n\t\"os"

	var b strings.Builder

	err := renderMainGo(&b, cfg, malicious)
	require.Error(t, err, "renderMainGo should reject ImportPath with injection characters")
}

func TestIsValidImportPathRejectsControlBytes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path string
		want bool
	}{
		"plain path": {"example.com/myapp/pkg", true},
		"empty":      {"", false},
		"null byte":  {"example.com/\x00pkg", false},
		"DEL byte":   {"example.com/\x7fpkg", false},
		"C1 low":     {"example.com/\u0080pkg", false},
		"C1 high":    {"example.com/\u009fpkg", false},
		// 0xa0 (NBSP) is the first code point above the C1 range, so it is not a
		// control character and the permissive check leaves it alone.
		"just above C1": {"example.com/\u00a0pkg", true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, isValidImportPath(tc.path))
		})
	}
}

func TestGoVersionPatternAnchored(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		version string
		want    []string // nil means no match; otherwise [major, minor].
	}{
		"release":           {"go1.26.0", []string{"1", "26"}},
		"release candidate": {"go1.25rc1", []string{"1", "25"}},
		"devel build":       {"devel go1.26-abc123 X:Y", []string{"1", "26"}},
		// The anchor reads only the leading version: a goMAJOR.MINOR embedded
		// later, or a leading token that merely ends in "go", does not match.
		"leading garbage":     {"prefix go1.9", nil},
		"embedded after word": {"cargo1.5", nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := goVersionPattern.FindStringSubmatch(tc.version)
			if tc.want == nil {
				assert.Nil(t, m)

				return
			}

			require.Len(t, m, 3)
			assert.Equal(t, tc.want, []string{m[1], m[2]})
		})
	}
}

func TestGoModUsesDetectedGoVersion(t *testing.T) {
	t.Parallel()

	goMod := renderGoMod("example.com/mymod", "/tmp/mymod", "/tmp/jsonschema")

	// The go directive is derived from the running toolchain via
	// goDirectiveVersion, never hardcoded.
	assert.Contains(t, goMod, "go "+goDirectiveVersion()+"\n",
		"go.mod go directive should be derived from the running toolchain")
}

func TestTestHelperGoModUsesDetectedGoVersion(t *testing.T) {
	t.Parallel()

	// CreateTestModule derives its go directive from goDirectiveVersion, the
	// same source the production code uses, so the helper never pins a fixed
	// version of its own.
	dir := createTestModule(t, `package testmod

type Stub struct{}
`)
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	require.NoError(t, err)

	assert.Contains(t, string(data), "go "+goDirectiveVersion()+"\n",
		"test helper go.mod go directive should be derived from the running toolchain")
}

func TestRenderGoModPathsWithSpaces(t *testing.T) {
	t.Parallel()

	goMod := renderGoMod(
		"example.com/mymod",
		"/Users/my user/project",
		"/Users/my user/jsonschema",
	)

	// RenderGoMod quotes replace-directive paths that contain whitespace, since
	// an unquoted path with a space does not parse in go.mod.
	assert.NotContains(t, goMod, "=> /Users/my user/project\n",
		"paths with spaces in replace directives should be quoted")

	assert.Contains(t, goMod, `"/Users/my user/project"`,
		"paths with spaces should be quoted in go.mod replace directives")
}

func TestRenderGoModEmitsTwoReplaceDirectives(t *testing.T) {
	t.Parallel()

	// RenderGoMod emits exactly two replace directives: one pointing the user's
	// module at its local directory and one pointing jsonschema at its local
	// directory.
	goMod := renderGoMod("example.com/mymod", "/tmp/mymod", "/tmp/jsonschema")

	lines := strings.Split(goMod, "\n")

	replaceCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "replace ") {
			replaceCount++
		}
	}

	assert.Equal(t, 2, replaceCount,
		"renderGoMod should emit replace directives for the user module and jsonschema")
}

func TestCheckReplaceDir(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		dir string
		err string
	}{
		"valid path":   {dir: "/tmp/mymod", err: ""},
		"empty dir":    {dir: "", err: "reported no local directory"},
		"backslash":    {dir: `/tmp/weird\dir`, err: "backslash"},
		"valid spaces": {dir: "/tmp/my mod", err: ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := checkReplaceDir("module directory", tc.dir)
			if tc.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

func TestCmdErrorExtractsStderr(t *testing.T) {
	t.Parallel()

	// CmdError surfaces the trimmed stderr in the message while wrapping the
	// original *exec.ExitError, so the stderr text is visible and the exit error
	// stays recoverable via errors.As.
	stderr := []byte("some error output")
	exitErr := &exec.ExitError{Stderr: stderr}

	wrapped := cmdError(exitErr)

	var extracted *exec.ExitError

	require.ErrorAs(t, wrapped, &extracted,
		"cmdError wraps the original *exec.ExitError so it stays recoverable")

	assert.Contains(t, wrapped.Error(), "some error output",
		"stderr content should be preserved in the error message")
}

func TestIntegrationInvalidTypeVerifyErrorMessage(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Exists struct{}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "DoesNotExist")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "DoesNotExist",
		"error message should mention the invalid type name")
}

func TestIntegrationCombinedFlags(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

// Config holds application configuration.
type Config struct {
	Name  string `+"`"+`json:"name" validate:"required,min=1"`+"`"+`
	Debug bool   `+"`"+`json:"debug"`+"`"+`
}
`)

	// Combine multiple flags: -draft 7 -validate -comments -additional-properties.
	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config", "-draft", "7",
		"-validate", "-comments", "-additional-properties")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	output := string(out)
	// Should produce a valid schema with all flags applied.
	assert.Contains(t, output, "draft-07",
		"output should use draft-07 schema URI")
	assert.Contains(t, output, "description",
		"output should include comments as descriptions")
	assert.Contains(t, output, "minLength",
		"output should include validate tag constraints")
	// Should NOT contain additionalProperties:false since -additional-properties was set.
	assert.NotContains(t, output, `"additionalProperties": false`,
		"output should allow additional properties")
}

func TestIntegrationOutputNonExistentDirectory(t *testing.T) {
	t.Parallel()

	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Simple struct {
	Name string `+"`"+`json:"name"`+"`"+`
}
`)

	// -o pointing to a non-existent directory.
	cmd := exec.CommandContext(t.Context(), binary, "-type", "Simple", "-o", "/tmp/nonexistent-dir-12345/schema.json")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.Error(t, err,
		"writing to non-existent directory should produce an error")
	assert.Contains(t, string(out), "nonexistent-dir-12345",
		"error message should reference the non-existent path")
}
