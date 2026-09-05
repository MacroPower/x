package goast_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/goast"
)

// writeModule writes a one-file module into a fresh directory and returns it.
func writeModule(t *testing.T, pkg, src string) string {
	t.Helper()

	dir := t.TempDir()
	goMod := "module example.com/" + pkg + "\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, pkg+".go"), []byte(src), 0o600))

	return dir
}

// TestLoadPackageFiles pins the two load outcomes the comment provider's
// cache depends on. A main package is located through the load directory,
// since reflection reports the import path "main" that no pattern resolves;
// and a load that parsed nothing and reported an error is not definitive, so
// it stays out of the cache for a later call to retry. Both used to cache an
// empty result for the process's lifetime.
//
//nolint:paralleltest // t.Setenv forbids a parallel parent, and the subtests share it.
func TestLoadPackageFiles(t *testing.T) {
	t.Setenv("GOWORK", "off")

	mainDir := writeModule(t, "main", `package main

// Config holds the settings.
type Config struct{}

func main() {}
`)
	libDir := writeModule(t, "lib", `package lib

// Config holds the settings.
type Config struct{}
`)

	t.Run("main package through the load directory", func(t *testing.T) {
		files, loaded, err := goast.LoadPackageFiles(t.Context(), mainDir, "main")
		require.NoError(t, err)
		require.True(t, loaded)
		require.Len(t, files, 1)

		doc, found := goast.TypeDoc(files, "Config")
		require.True(t, found)
		assert.Equal(t, "Config holds the settings.", doc)
	})

	t.Run("main package in a non-main directory", func(t *testing.T) {
		files, loaded, err := goast.LoadPackageFiles(t.Context(), libDir, "main")
		require.NoError(t, err)
		assert.True(t, loaded, "a non-main load directory is definitive")
		assert.Empty(t, files)
	})

	t.Run("unresolvable path is not definitive", func(t *testing.T) {
		files, loaded, err := goast.LoadPackageFiles(t.Context(), libDir, "example.com/does/not/exist")
		require.NoError(t, err)
		assert.False(t, loaded, "a failed load stays out of the cache")
		assert.Empty(t, files)
	})
}
