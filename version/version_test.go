package version_test

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/version"
)

// Tests that mutate package-level variables (Version, Revision) must
// save/restore via t.Cleanup and must not use t.Parallel because the package
// variables are global state shared across all tests in the process.

//nolint:paralleltest // Mutates package-level Version.
func TestRelease(t *testing.T) {
	tests := map[string]struct {
		version     string
		wantVersion string
		wantOK      bool
	}{
		"tagged release": {
			version:     "v1.2.3",
			wantVersion: "v1.2.3",
			wantOK:      true,
		},
		"empty version is dev build": {
			version:     "",
			wantVersion: "",
			wantOK:      false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			orig := version.Version
			t.Cleanup(func() { version.Version = orig })

			version.Version = tc.version

			got, ok := version.Release()
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantVersion, got)
		})
	}
}

//nolint:paralleltest // Mutates package-level Version and Revision.
func TestGetVersion(t *testing.T) {
	tests := map[string]struct {
		version  string
		revision string
		want     string
	}{
		"ldflags version takes priority": {
			version:  "v1.0.0",
			revision: "abc1234",
			want:     "v1.0.0",
		},
		"known revision produces devel- prefix": {
			version:  "",
			revision: "abc1234",
			want:     "devel-abc1234",
		},
		"dirty revision preserved": {
			version:  "",
			revision: "abc1234-dirty",
			want:     "devel-abc1234-dirty",
		},
		"unknown revision falls back to devel": {
			version:  "",
			revision: "unknown",
			want:     "devel",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			origV := version.Version
			origR := version.Revision
			t.Cleanup(func() {
				version.Version = origV
				version.Revision = origR
			})

			version.Version = tc.version
			version.Revision = tc.revision

			got := version.GetVersion()
			assert.Equal(t, tc.want, got)
		})
	}
}

//nolint:paralleltest // Mutates package-level variables.
func TestGet(t *testing.T) {
	origVersion := version.Version
	origBranch := version.Branch
	origBuildUser := version.BuildUser
	origBuildDate := version.BuildDate
	origRevision := version.Revision
	t.Cleanup(func() {
		version.Version = origVersion
		version.Branch = origBranch
		version.BuildUser = origBuildUser
		version.BuildDate = origBuildDate
		version.Revision = origRevision
	})

	version.Version = "v2.0.0"
	version.Branch = "main"
	version.BuildUser = "alice"
	version.BuildDate = "2024-01-01"
	version.Revision = "abc1234"

	info := version.Get()

	assert.Equal(t, "v2.0.0", info.Version)
	assert.Equal(t, "main", info.Branch)
	assert.Equal(t, "alice", info.BuildUser)
	assert.Equal(t, "2024-01-01", info.BuildDate)
	assert.Equal(t, "abc1234", info.Revision)
	assert.Equal(t, runtime.Version(), info.GoVersion)
	assert.Equal(t, runtime.GOOS, info.GoOS)
	assert.Equal(t, runtime.GOARCH, info.GoArch)
}

func TestInfoString(t *testing.T) {
	t.Parallel()

	goVer := runtime.Version()
	goOS := runtime.GOOS
	goArch := runtime.GOARCH

	tests := map[string]struct {
		info version.Info
		want string
	}{
		"full release build": {
			info: version.Info{
				Version:   "v1.2.3",
				Revision:  "abc1234",
				Branch:    "main",
				BuildUser: "alice@builder",
				BuildDate: "2024-06-01",
				GoVersion: goVer,
				GoOS:      goOS,
				GoArch:    goArch,
			},
			want: stringtest.JoinLF(
				"version v1.2.3",
				"  revision: abc1234",
				"  branch: main",
				"  build user: alice@builder",
				"  build date: 2024-06-01",
				"  go: "+goVer+" "+goOS+"/"+goArch,
			),
		},
		"dev build omits empty optional fields": {
			info: version.Info{
				Version:   "",
				Revision:  "abc1234",
				Branch:    "",
				BuildUser: "",
				BuildDate: "",
				GoVersion: goVer,
				GoOS:      goOS,
				GoArch:    goArch,
			},
			want: stringtest.JoinLF(
				"version abc1234",
				"  revision: abc1234",
				"  go: "+goVer+" "+goOS+"/"+goArch,
			),
		},
		"version present branch absent": {
			info: version.Info{
				Version:   "v0.1.0",
				Revision:  "deadbee",
				Branch:    "",
				BuildUser: "bob",
				BuildDate: "",
				GoVersion: goVer,
				GoOS:      goOS,
				GoArch:    goArch,
			},
			want: stringtest.JoinLF(
				"version v0.1.0",
				"  revision: deadbee",
				"  build user: bob",
				"  go: "+goVer+" "+goOS+"/"+goArch,
			),
		},
		"empty revision omits the revision line": {
			info: version.Info{
				Version:   "v1.0.0",
				GoVersion: goVer,
				GoOS:      goOS,
				GoArch:    goArch,
			},
			want: stringtest.JoinLF(
				"version v1.0.0",
				"  go: "+goVer+" "+goOS+"/"+goArch,
			),
		},
		"version empty revision unknown": {
			info: version.Info{
				Version:   "",
				Revision:  "unknown",
				Branch:    "",
				BuildUser: "",
				BuildDate: "",
				GoVersion: goVer,
				GoOS:      goOS,
				GoArch:    goArch,
			},
			want: stringtest.JoinLF(
				"version unknown",
				"  revision: unknown",
				"  go: "+goVer+" "+goOS+"/"+goArch,
			),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := tc.info.String()
			require.Equal(t, tc.want, got)
		})
	}
}

func TestInfoStringNoTrailingNewline(t *testing.T) {
	t.Parallel()

	info := version.Info{
		Version:   "v1.0.0",
		Revision:  "abc1234",
		GoVersion: runtime.Version(),
		GoOS:      runtime.GOOS,
		GoArch:    runtime.GOARCH,
	}

	got := info.String()
	require.NotEmpty(t, got)
	assert.NotEqual(t, '\n', rune(got[len(got)-1]), "String must not end with a newline")
}
