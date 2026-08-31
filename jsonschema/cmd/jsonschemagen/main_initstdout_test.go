package main

import (
	"bytes"
	"encoding/json/jsontext"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initStdoutModule is a target package whose init prints to stdout, the way a
// chatty transitive dependency's debug banner would. The helper imports the
// target package, so the print runs before jsonschema.Generate; it must never
// reach the schema data channel.
const initStdoutModule = `package testmod

import "fmt"

func init() { fmt.Println("boot message") }

type Config struct {
	Name string ` + "`json:\"name\"`" + `
}
`

// initStdoutWant is the schema the polluting module must still generate.
const initStdoutWant = `{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"type": "object",
	"properties": {"name": {"type": "string"}},
	"required": ["name"],
	"additionalProperties": false
}`

func TestIntegrationInitStdoutKeptOutOfSchemaOutput(t *testing.T) {
	t.Parallel()

	// Init-time stdout from the target package must not be prepended to the
	// schema written to the tool's stdout; it is forwarded to stderr instead.
	binary := buildBinary(t)
	dir := createTestModule(t, initStdoutModule)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config")
	cmd.Dir = dir

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", stderr.String())

	require.True(t, jsontext.Value(out).IsValid(),
		"stdout must be valid JSON, got: %s", out)
	assert.JSONEq(t, initStdoutWant, string(out))
	assert.Contains(t, stderr.String(), "boot message",
		"stray init output is forwarded to stderr, not swallowed")
}

func TestIntegrationInitStdoutKeptOutOfSchemaFile(t *testing.T) {
	t.Parallel()

	// The same pollution must not corrupt a -o file: the tool previously exited
	// 0 while committing "boot message" followed by the JSON to the output file.
	binary := buildBinary(t)
	dir := createTestModule(t, initStdoutModule)

	outFile := filepath.Join(t.TempDir(), "config.schema.json")
	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config", "-o", outFile)
	cmd.Dir = dir

	cmdOut, err := cmd.CombinedOutput()
	require.NoError(t, err, "output: %s", cmdOut)

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)

	require.True(t, jsontext.Value(data).IsValid(),
		"the output file must be valid JSON, got: %s", data)
	assert.JSONEq(t, initStdoutWant, string(data))
}
