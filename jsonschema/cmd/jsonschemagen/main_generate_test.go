package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeModuleFiles writes a go.mod for modPath that requires and locally
// replaces jsonschema, and copies jsonschema's go.sum so transitive deps
// resolve. Callers add their own package files.
func writeModuleFiles(t *testing.T, dir, modPath string) {
	t.Helper()

	jsDir := moduleDir(t)
	goMod := "module " + modPath + "\n\ngo " + testGoVersion +
		"\n\nrequire go.jacobcolvin.com/x/jsonschema v0.0.0\n\n" +
		"replace go.jacobcolvin.com/x/jsonschema => " + jsDir + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644))

	data, err := os.ReadFile(filepath.Join(jsDir, "go.sum"))
	if err == nil {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.sum"), data, 0o644))
	}
}

func TestIntegrationInternalPackageTarget(t *testing.T) {
	t.Parallel()

	// The helper is placed under the target package directory, so it is a
	// descendant of every internal/ parent the target sits beneath and the
	// import is allowed. A helper at the module root could not import an
	// internal/ target.
	binary := buildBinary(t)
	dir := t.TempDir()
	writeModuleFiles(t, dir, "example.com/app")

	pkgDir := filepath.Join(dir, "svc", "internal", "foo")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "foo.go"), []byte(`package foo

type Secret struct {
	Token string `+"`"+`json:"token"`+"`"+`
}
`), 0o644))

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Secret")
	cmd.Dir = pkgDir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {"token": {"type": "string"}},
		"required": ["token"],
		"additionalProperties": false
	}`, string(out))
}

func TestIntegrationCommentsSeedsMissingChecksums(t *testing.T) {
	t.Parallel()

	// -comments pulls golang.org/x/tools into the helper's build graph. A user
	// module whose go.sum lacks those checksums must still generate: the seed
	// step completes them into a redirected modfile from the module cache,
	// leaving the user's own go.sum untouched.
	binary := buildBinary(t)
	jsDir := moduleDir(t)
	dir := t.TempDir()

	goMod := "module example.com/app\n\ngo " + testGoVersion +
		"\n\nrequire go.jacobcolvin.com/x/jsonschema v0.0.0\n\n" +
		"replace go.jacobcolvin.com/x/jsonschema => " + jsDir + "\n"
	modPath := filepath.Join(dir, "go.mod")
	require.NoError(t, os.WriteFile(modPath, []byte(goMod), 0o644))

	// A go.sum without the golang.org/x/tools entries, to force the seed step.
	full, err := os.ReadFile(filepath.Join(jsDir, "go.sum"))
	require.NoError(t, err)

	var trimmed strings.Builder

	for line := range strings.SplitSeq(string(full), "\n") {
		if line == "" || strings.Contains(line, "golang.org/x/tools") {
			continue
		}

		trimmed.WriteString(line)
		trimmed.WriteByte('\n')
	}

	sumPath := filepath.Join(dir, "go.sum")
	require.NoError(t, os.WriteFile(sumPath, []byte(trimmed.String()), 0o644))

	beforeSum, err := os.ReadFile(sumPath)
	require.NoError(t, err)

	beforeMod, err := os.ReadFile(modPath)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "types.go"), []byte(`package app

// Config is the application configuration.
type Config struct {
	Name string `+"`"+`json:"name"`+"`"+`
}
`), 0o644))

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config", "-comments")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))
	assert.Contains(t, string(out), "Config is the application configuration.")

	afterSum, err := os.ReadFile(sumPath)
	require.NoError(t, err)
	assert.Equal(t, beforeSum, afterSum, "seeding must not modify the user's go.sum")

	afterMod, err := os.ReadFile(modPath)
	require.NoError(t, err)
	assert.Equal(t, beforeMod, afterMod, "generation must not modify the user's go.mod")
}

func TestIntegrationOfflineWithWarmCache(t *testing.T) {
	t.Parallel()

	// The offline guarantee: with the module proxy off, a module that resolves
	// jsonschema through a local replace (its transitive dependencies already in
	// the module cache from building this repo) must still generate. No step in
	// the pipeline -- import-path resolution, modfile seeding, the helper build
	// -- may reach for the network.
	binary := buildBinary(t)
	dir := t.TempDir()
	writeModuleFiles(t, dir, "example.com/app")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "types.go"), []byte(`package app

// Config is the application configuration.
type Config struct {
	Name string `+"`"+`json:"name"`+"`"+`
}
`), 0o644))

	// -comments forces the seed step to complete golang.org/x/tools into the
	// redirected modfile, so the offline guarantee covers `go get` as well.
	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config", "-comments")
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=")

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))
	assert.Contains(t, string(out), "Config is the application configuration.")
}

func TestIntegrationVendorMode(t *testing.T) {
	t.Parallel()

	// A vendor/modules.txt auto-selects vendor mode, which the helper's
	// dependencies are absent from. Generation must still succeed by building in
	// module mode via the redirected modfile.
	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Config struct {
	Name string `+"`"+`json:"name"`+"`"+`
}
`)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "vendor"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "vendor", "modules.txt"),
		[]byte("# example.com/testmod\n"), 0o644))

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {"name": {"type": "string"}},
		"required": ["name"],
		"additionalProperties": false
	}`, string(out))
}

func TestIntegrationMissingJsonschemaDependency(t *testing.T) {
	t.Parallel()

	// A module that does not depend on jsonschema cannot resolve the helper's
	// import. With the proxy off (so it cannot be silently fetched), the tool
	// must surface a clear, mapped error with a remediation hint rather than a
	// raw go-tool dump.
	binary := buildBinary(t)
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/app\n\ngo "+testGoVersion+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "types.go"), []byte(`package app

type Config struct {
	Name string `+"`"+`json:"name"`+"`"+`
}
`), 0o644))

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config")
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=")

	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "go mod tidy",
		"the error must include the remediation hint, not just the raw go dump")
	assert.Contains(t, string(out), jsonschemaModule,
		"the hint must name the unresolved module")
}

func TestIntegrationLeavesNoArtifacts(t *testing.T) {
	t.Parallel()

	// The helper is supplied through a build overlay, so no .jsonschemagen-*
	// directory is ever written into the target package's directory.
	binary := buildBinary(t)
	dir := createTestModule(t, `package testmod

type Config struct {
	Name string `+"`"+`json:"name"`+"`"+`
}
`)

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Config")
	cmd.Dir = dir

	_, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".jsonschemagen-"),
			"no helper directory should remain in the source tree: %s", e.Name())
	}
}

func TestIntegrationWorkspaceMode(t *testing.T) {
	t.Parallel()

	// Under a workspace the tool skips modfile seeding and relies on the
	// workspace's own module graph and go.work.sum. A go.work with a local
	// replace of jsonschema, plus the repo's go.work.sum for the transitive
	// checksums, resolves offline.
	binary := buildBinary(t)
	jsDir := moduleDir(t)
	repoRoot := filepath.Dir(jsDir)
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module example.com/wsapp

go `+testGoVersion+`

require go.jacobcolvin.com/x/jsonschema v0.0.0
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "types.go"), []byte(`package wsapp

type Item struct {
	ID string `+"`"+`json:"id"`+"`"+`
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.work"), []byte(`go `+testGoVersion+`

use .

replace go.jacobcolvin.com/x/jsonschema => `+jsDir+`
`), 0o644))

	// Reuse the repo's go.work.sum so jsonschema's transitive deps verify offline.
	data, err := os.ReadFile(filepath.Join(repoRoot, "go.work.sum"))
	if err == nil {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.work.sum"), data, 0o644))
	}

	cmd := exec.CommandContext(t.Context(), binary, "-type", "Item")
	cmd.Dir = dir

	out, err := cmd.Output()
	require.NoError(t, err, "stderr: %s", cmdStderr(err))

	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {"id": {"type": "string"}},
		"required": ["id"],
		"additionalProperties": false
	}`, string(out))
}
