package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

	var info struct {
		Dir string `json:"Dir"`
	}

	require.NoError(t, json.Unmarshal(out, &info))

	return info.Dir
}

// testGoVersion is the go directive for generated test modules. It must be at
// least the jsonschema module's own go directive (they require jsonschema), so
// it is pinned rather than derived from the running toolchain.
const testGoVersion = "1.26.0"

// createTestModule creates a temporary Go module with the given type definition
// and returns the module directory.
func createTestModule(t *testing.T, typeDef string) string {
	t.Helper()

	dir := t.TempDir()
	jsDir := moduleDir(t)

	goMod := `module example.com/testmod

go ` + testGoVersion + `

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

func TestIntegrationPackageMainRejected(t *testing.T) {
	t.Parallel()

	// A type in a package main cannot be reflected over, because jsonschemagen
	// imports the target package and Go forbids importing a main package. The
	// tool must reject it with a clear message rather than failing late with the
	// opaque "is a program, not an importable package" build error.
	binary := buildBinary(t)
	dir := createTestModule(t, `package main

type Config struct {
	Name string `+"`"+`json:"name"`+"`"+`
}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "package main",
		"a main-package target must be rejected with a clear message")
	assert.NotContains(t, string(out), "not an importable package",
		"the opaque go build error must not leak through")
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
