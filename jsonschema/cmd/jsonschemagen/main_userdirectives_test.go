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

func TestUserGoModDirectives(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		goMod   string
		modDir  string
		modPath string
		want    []string
		err     string
	}{
		"no directives": {
			goMod:   "module example.com/app\n\ngo 1.21\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want:    nil,
		},
		"relative directory target resolved against modDir": {
			goMod: "module example.com/app\n\ngo 1.21\n\n" +
				"replace example.com/dep => ../dep\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want:    []string{"replace example.com/dep => /home/user/dep"},
		},
		"absolute directory target passes through": {
			goMod: "module example.com/app\n\ngo 1.21\n\n" +
				"replace example.com/dep => /opt/dep\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want:    []string{"replace example.com/dep => /opt/dep"},
		},
		"directory target with a space is quoted": {
			goMod: "module example.com/app\n\ngo 1.21\n\n" +
				"replace example.com/dep => \"/opt/my dep\"\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want:    []string{`replace example.com/dep => "/opt/my dep"`},
		},
		"module target copies verbatim": {
			goMod: "module example.com/app\n\ngo 1.21\n\n" +
				"replace example.com/dep v1.0.0 => example.com/fork v1.1.0\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want:    []string{"replace example.com/dep v1.0.0 => example.com/fork v1.1.0"},
		},
		"replace of the user module is skipped": {
			goMod: "module example.com/other\n\ngo 1.21\n\n" +
				"replace example.com/app => ../app-fork\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want:    nil,
		},
		"replace of jsonschema is skipped": {
			goMod: "module example.com/app\n\ngo 1.21\n\n" +
				"replace go.jacobcolvin.com/x/jsonschema => ../jsonschema\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want:    nil,
		},
		"exclude copies verbatim": {
			goMod: "module example.com/app\n\ngo 1.21\n\n" +
				"exclude example.com/old v1.0.0\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want:    []string{"exclude example.com/old v1.0.0"},
		},
		"mixed directives keep replaces then excludes": {
			goMod: "module example.com/app\n\ngo 1.21\n\n" +
				"exclude example.com/old v1.0.0\n\n" +
				"replace (\n" +
				"\texample.com/app => ../self\n" +
				"\texample.com/dep => ./vendor-dep\n" +
				"\texample.com/fork v1.0.0 => example.com/upstream v1.1.0\n" +
				")\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			want: []string{
				"replace example.com/dep => /home/user/app/vendor-dep",
				"replace example.com/fork v1.0.0 => example.com/upstream v1.1.0",
				"exclude example.com/old v1.0.0",
			},
		},
		"unparsable go.mod": {
			goMod:   "module example.com/app\n\nbogus directive\n",
			modDir:  "/home/user/app",
			modPath: "example.com/app",
			err:     "parse go.mod",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := userGoModDirectives([]byte(tc.goMod), tc.modDir, tc.modPath)
			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestUserGoModDirectivesRejectsBackslashTarget(t *testing.T) {
	t.Parallel()

	if filepath.Separator != '/' {
		t.Skip("a backslash directory target is only rejected on slash-separator hosts")
	}

	// A backslash directory target is rejected when the user's go.mod is
	// parsed (the modfile parser refuses a Windows-looking replacement path on
	// a non-Windows system), so it surfaces as a clear parse error at the
	// input boundary rather than a cryptic go mod tidy failure in the temp
	// module.
	goMod := "module example.com/app\n\ngo 1.21\n\n" +
		`replace example.com/dep => /opt/weird\dep` + "\n"

	_, err := userGoModDirectives([]byte(goMod), "/home/user/app", "example.com/app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse go.mod")
}

func TestRenderGoModAppendsUserDirectives(t *testing.T) {
	t.Parallel()

	got := renderGoMod(
		"example.com/app",
		"/home/user/app",
		"/home/user/jsonschema",
		"replace example.com/dep => /home/user/dep",
		"exclude example.com/old v1.0.0",
	)

	assert.True(t, strings.HasSuffix(got,
		"replace go.jacobcolvin.com/x/jsonschema => /home/user/jsonschema\n"+
			"replace example.com/dep => /home/user/dep\n"+
			"exclude example.com/old v1.0.0\n"),
		"user directives must follow the tool's own replace directives, got:\n%s", got)
}

func TestIntegrationUserReplaceDirective(t *testing.T) {
	t.Parallel()

	// Replace directives apply only to the main module of a build, so the temp
	// module must copy the user's replaces: without them, a replace pointing at
	// a local unpublished module makes go mod tidy try to fetch the original
	// path from the network, and a replace pointing at a fork silently
	// generates from the unreplaced upstream code.
	binary := buildBinary(t)
	jsDir := moduleDir(t)

	parent := t.TempDir()
	depDir := filepath.Join(parent, "dep")
	userDir := filepath.Join(parent, "app")

	require.NoError(t, os.MkdirAll(depDir, 0o755))
	require.NoError(t, os.MkdirAll(userDir, 0o755))

	// The replaced dependency: a local module that does not exist upstream.
	require.NoError(t, os.WriteFile(filepath.Join(depDir, "go.mod"), []byte(`module example.com/dep

go `+goDirectiveVersion()+`
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(depDir, "options.go"), []byte(`package dep

type Options struct {
	Extra string `+"`"+`json:"extra"`+"`"+`
}
`), 0o644))

	// The user's module replaces the dependency with a relative directory, so
	// the copied directive must also be resolved against the module directory.
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "go.mod"), []byte(`module example.com/testmod

go `+goDirectiveVersion()+`

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
