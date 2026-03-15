package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"go.jacobcolvin.com/jsonschema"

	target "example.com/myapp"
)

func main() {
	t := reflect.TypeFor[target.Config]()
	opts := []jsonschema.Option{
	}
	schema, err := jsonschema.Generate(t, opts...)
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
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"go.jacobcolvin.com/jsonschema"

	target "example.com/pkg"
)

func main() {
	t := reflect.TypeFor[target.Settings]()
	opts := []jsonschema.Option{
		jsonschema.WithDraft(jsonschema.Draft7),
	}
	schema, err := jsonschema.Generate(t, opts...)
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
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"go.jacobcolvin.com/jsonschema"
	"go.jacobcolvin.com/jsonschema/interpreters/validate"

	target "example.com/full"
)

func main() {
	t := reflect.TypeFor[target.MyType]()
	opts := []jsonschema.Option{
		jsonschema.WithComments(true),
		jsonschema.WithAdditionalProperties(true),
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	}
	schema, err := jsonschema.Generate(t, opts...)
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
		modPath      string
		modDir       string
		jsonschmaDir string
		want         string
	}{
		"different module": {
			modPath:      "example.com/myapp",
			modDir:       "/home/user/myapp",
			jsonschmaDir: "/home/user/go/pkg/mod/go.jacobcolvin.com/jsonschema@v0.1.0",
			want: fmt.Sprintf(`module _jsonschemagen_tmp

go %s

require (
	example.com/myapp v0.0.0
	go.jacobcolvin.com/jsonschema v0.0.0
)

replace example.com/myapp => /home/user/myapp
replace go.jacobcolvin.com/jsonschema => /home/user/go/pkg/mod/go.jacobcolvin.com/jsonschema@v0.1.0
`, goDirectiveVersion()),
		},
		"jsonschema module itself": {
			modPath:      "go.jacobcolvin.com/jsonschema",
			modDir:       "/home/user/jsonschema",
			jsonschmaDir: "/home/user/jsonschema",
			want: fmt.Sprintf(`module _jsonschemagen_tmp

go %s

require (
	go.jacobcolvin.com/jsonschema v0.0.0
)

replace go.jacobcolvin.com/jsonschema => /home/user/jsonschema
`, goDirectiveVersion()),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := renderGoMod(tc.modPath, tc.modDir, tc.jsonschmaDir)
			assert.Equal(t, tc.want, got)
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

	cmd := exec.CommandContext(t.Context(), "go", "list", "-m", "-json", "go.jacobcolvin.com/jsonschema")
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

require go.jacobcolvin.com/jsonschema v0.0.0

replace go.jacobcolvin.com/jsonschema => ` + jsDir + `
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

	var exitErr *exec.ExitError
	if ok := errors.As(err, &exitErr); ok {
		return string(exitErr.Stderr)
	}

	return err.Error()
}

func TestTypeNameNotValidatedForInjection(t *testing.T) {
	t.Parallel()

	// The -type flag value is injected directly into a Go source template.
	// A crafted TypeName could inject arbitrary code. Low severity since
	// the CLI is intended for go:generate use in trusted codebases.

	// TypeName with special characters that could break the template.
	cfg := config{
		TypeName: "Foo]()\n\tos.Exit(0)\n\t//",
		Draft:    "2020",
		Indent:   "  ",
	}

	var b strings.Builder

	err := renderMainGo(&b, cfg, "example.com/myapp")
	// The generated source should either be rejected or produce invalid Go.
	// Currently it renders without error, producing compilable injected code.
	require.Error(t, err, "renderMainGo should reject TypeName with special characters")
}

func TestImportPathNotValidatedForInjection(t *testing.T) {
	t.Parallel()

	// ImportPath is inserted into the Go template as target "{{.ImportPath}}".
	cfg := config{
		TypeName: "Foo",
		Draft:    "2020",
		Indent:   "  ",
	}

	// Crafted import path with quote to break the template.
	malicious := `example.com/myapp"` + "\n\t\"os"

	var b strings.Builder

	err := renderMainGo(&b, cfg, malicious)
	// Should either be rejected or produce invalid Go.
	require.Error(t, err, "renderMainGo should reject ImportPath with injection characters")
}

func TestHardcodedGoVersionInGoMod(t *testing.T) {
	t.Parallel()

	goMod := renderGoMod("example.com/mymod", "/tmp/mymod", "/tmp/jsonschema")

	// The go version should match the current Go version or be the module's
	// minimum, not a hardcoded "go 1.25.5".
	currentVersion := runtime.Version()
	_ = currentVersion

	assert.NotContains(t, goMod, "go 1.25.5",
		"go.mod should not hardcode a specific Go version; should detect from runtime or module")
}

func TestHardcodedGoVersionInTestHelper(t *testing.T) {
	t.Parallel()

	// CreateTestModule hardcodes "go 1.25.5" independently of the production code.
	// Both locations need the same fix: detect from runtime or module.
	dir := createTestModule(t, `package testmod

type Stub struct{}
`)
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	require.NoError(t, err)

	// When fixed, the go.mod should not contain "go 1.25.5".
	assert.NotContains(t, string(data), "go 1.25.5",
		"test helper go.mod should not hardcode a specific Go version")
}

func TestGoModTidyErrorNotWrapped(t *testing.T) {
	t.Parallel()

	// RunGenerate uses %s instead of %w for the go mod tidy error,
	// discarding the original *exec.ExitError.
	//
	// Simulate the difference between %s and %w wrapping.
	original := &exec.ExitError{Stderr: []byte("some output")}

	// This is what the code currently does (uses %s):.
	wrappedWithS := fmt.Errorf("go mod tidy: %s", []byte("some output"))

	// This is what the code should do (use %w):.
	wrappedWithW := fmt.Errorf("go mod tidy: %w", original)

	// With %s, the original error identity is lost.
	var extracted *exec.ExitError
	assert.NotErrorAs(t, wrappedWithS, &extracted,
		"%%s wrapping loses the original *exec.ExitError")
	assert.ErrorAs(t, wrappedWithW, &extracted,
		"%%w wrapping preserves the original *exec.ExitError")
}

func TestGoSumWriteFailureSilentlyIgnored(t *testing.T) {
	t.Parallel()

	// If writing go.sum to the temp dir fails, the error is discarded.
	// The subsequent go mod tidy might download from network or fail.
	//
	// Demonstrating the issue: main.go:178-179 reads go.sum and writes it
	// with `_ = os.WriteFile(...)`, discarding the write error. The fix
	// should propagate write errors from os.WriteFile.
	//
	// This is a code review finding that cannot be demonstrated with a unit
	// test without filesystem injection. The fix is to check the error from
	// os.WriteFile at main.go:179.
	t.Log("go.sum write error at main.go:179 is assigned to _ and never checked")
}

func TestRenderGoModPathsWithSpaces(t *testing.T) {
	t.Parallel()

	goMod := renderGoMod(
		"example.com/mymod",
		"/Users/my user/project",
		"/Users/my user/jsonschema",
	)

	// Go.mod replace directives require quoted paths for paths with spaces.
	// Currently the paths are injected without quoting.
	assert.NotContains(t, goMod, "=> /Users/my user/project\n",
		"paths with spaces in replace directives should be quoted")

	// Should be: replace example.com/mymod => "/Users/my user/project".
	assert.Contains(t, goMod, `"/Users/my user/project"`,
		"paths with spaces should be quoted in go.mod replace directives")
}

func TestRenderGoModMissingReplaceDirectives(t *testing.T) {
	t.Parallel()

	// The generated go.mod only requires the user's module and jsonschema.
	// If the user's module has replace directives for transitive deps,
	// those are NOT propagated. Go mod tidy would fail for modules
	// with local replacements.
	goMod := renderGoMod("example.com/mymod", "/tmp/mymod", "/tmp/jsonschema")

	// The go.mod should ideally propagate the user module's replace directives.
	// Currently it only has the two replace directives for the user module
	// and jsonschema.
	lines := strings.Split(goMod, "\n")

	replaceCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "replace ") {
			replaceCount++
		}
	}
	// Only 2 replace directives -- user module and jsonschema.
	// No mechanism to propagate transitive deps.
	assert.Equal(t, 2, replaceCount,
		"documents that only 2 replace directives are generated (no transitive propagation)")
}

func TestCmdErrorLosesExitCode(t *testing.T) {
	t.Parallel()

	// CmdError extracts stderr from *exec.ExitError and wraps it as a string.
	// The original *exec.ExitError (with its exit code) is lost.
	stderr := []byte("some error output")
	exitErr := &exec.ExitError{Stderr: stderr}

	wrapped := cmdError(exitErr)

	// The wrapped error should still be identifiable as an exec.ExitError.
	var extracted *exec.ExitError
	assert.NotErrorAs(t, wrapped, &extracted,
		"documents that cmdError loses the original *exec.ExitError")

	// Verify the stderr content is preserved in the error message.
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
	// Should verify the error message mentions the type name.
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
