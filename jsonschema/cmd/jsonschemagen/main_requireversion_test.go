package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireVersion(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		modPath string
		want    string
	}{
		"no major suffix":     {modPath: "example.com/app", want: "v0.0.0"},
		"slash v2":            {modPath: "example.com/app/v2", want: "v2.0.0"},
		"slash v10":           {modPath: "example.com/app/v10", want: "v10.0.0"},
		"gopkg.in dot v2":     {modPath: "gopkg.in/app.v2", want: "v2.0.0"},
		"gopkg.in dot v0":     {modPath: "gopkg.in/app.v0", want: "v0.0.0"},
		"v2 as inner element": {modPath: "example.com/v2/app", want: "v0.0.0"},
		"jsonschema module":   {modPath: jsonschemaModule, want: "v0.0.0"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, requireVersion(tc.modPath))
		})
	}
}

func TestRenderGoModMajorVersionSuffix(t *testing.T) {
	t.Parallel()

	// The go.mod parser enforces that a require version's major version matches
	// the module path's major-version suffix, so a /vN (or gopkg.in .vN) module
	// path must be required at vN, not v0 -- a directory replace does not exempt
	// the require line from that parse-time check.
	tests := map[string]struct {
		modPath string
		want    string
	}{
		"plain path stays v0": {
			modPath: "example.com/app",
			want:    "\texample.com/app v0.0.0\n",
		},
		"slash v2 requires v2": {
			modPath: "example.com/app/v2",
			want:    "\texample.com/app/v2 v2.0.0\n",
		},
		"gopkg.in dot v3 requires v3": {
			modPath: "gopkg.in/app.v3",
			want:    "\tgopkg.in/app.v3 v3.0.0\n",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := renderGoMod(tc.modPath, "/home/user/app", "/home/user/jsonschema")
			assert.Contains(t, got, tc.want)
		})
	}
}

// createTestModuleWithPath creates a temporary Go module with the given module
// path and type definition and returns the module directory.
func createTestModuleWithPath(t *testing.T, modPath, typeDef string) string {
	t.Helper()

	dir := t.TempDir()
	jsDir := moduleDir(t)

	goMod := `module ` + modPath + `

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

func TestIntegrationMajorVersionSuffixModule(t *testing.T) {
	t.Parallel()

	// A module whose path carries a major-version suffix must generate like any
	// other: the temp go.mod requires it at the matching major version, so `go
	// mod tidy` parses it.
	binary := buildBinary(t)
	dir := createTestModuleWithPath(t, "example.com/testmod/v2", `package testmod

type Config struct {
	Name string `+"`"+`json:"name"`+"`"+`
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
			"name": {"type": "string"}
		},
		"required": ["name"],
		"additionalProperties": false
	}`, string(out))
}
