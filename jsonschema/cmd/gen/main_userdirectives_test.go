package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationUserReplaceDirective(t *testing.T) {
	t.Parallel()

	// The helper builds inside the user's own module, so the user's replace
	// directives apply natively with no copying by the CLI: a replace pointing
	// at a local unpublished module resolves to that directory rather than being
	// fetched from the network.
	binary := buildBinary(t)
	jsDir := moduleDir(t)

	parent := t.TempDir()
	depDir := filepath.Join(parent, "dep")
	userDir := filepath.Join(parent, "app")

	require.NoError(t, os.MkdirAll(depDir, 0o755))
	require.NoError(t, os.MkdirAll(userDir, 0o755))

	// The replaced dependency: a local module that does not exist upstream.
	require.NoError(t, os.WriteFile(filepath.Join(depDir, "go.mod"), []byte(`module example.com/dep

go `+testGoVersion+`
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(depDir, "options.go"), []byte(`package dep

type Options struct {
	Extra string `+"`"+`json:"extra"`+"`"+`
}
`), 0o644))

	// The user's module replaces the dependency with a relative directory, which
	// the go tool resolves against the module root when the helper builds there.
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "go.mod"), []byte(`module example.com/testmod

go `+testGoVersion+`

require (
	example.com/dep v0.0.0
	go.jacobcolvin.com/x/jsonschema v0.0.0
)

replace example.com/dep => ../dep

replace go.jacobcolvin.com/x/jsonschema => `+jsDir+`
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "types.go"), []byte(`package testmod

import "example.com/dep"

type Config struct {
	Name    string      `+"`"+`json:"name"`+"`"+`
	Options dep.Options `+"`"+`json:"options"`+"`"+`
}
`), 0o644))

	// Copy go.sum from the jsonschema module so transitive deps resolve.
	data, err := os.ReadFile(filepath.Join(jsDir, "go.sum"))
	if err == nil {
		require.NoError(t, os.WriteFile(filepath.Join(userDir, "go.sum"), data, 0o644))
	}

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config")
	cmd.Dir = userDir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"$defs": {
			"Options": {
				"type": "object",
				"properties": {
					"extra": {"type": "string"}
				},
				"required": ["extra"],
				"additionalProperties": false
			}
		},
		"properties": {
			"name": {"type": "string"},
			"options": {"$ref": "#/$defs/Options"}
		},
		"required": ["name", "options"],
		"additionalProperties": false
	}`, string(out))
}
