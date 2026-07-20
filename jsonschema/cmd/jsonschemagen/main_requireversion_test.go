package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestModuleWithPath creates a temporary Go module with the given module
// path and type definition and returns the module directory.
func createTestModuleWithPath(t *testing.T, modPath, typeDef string) string {
	t.Helper()

	dir := t.TempDir()
	jsDir := moduleDir(t)

	goMod := `module ` + modPath + `

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

func TestIntegrationMajorVersionSuffixModule(t *testing.T) {
	t.Parallel()

	// A module whose path carries a major-version suffix must generate like any
	// other. The helper builds inside the user's module, so the go tool resolves
	// the /vN path from the user's own go.mod with no version reconstruction by
	// the CLI.
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
