package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// revisionUnknown is the [Revision] placeholder when no VCS revision is
// stamped into the binary.
const revisionUnknown = "unknown"

var (
	// Version is the application version, set via ldflags.
	Version string
	// Branch is the git branch, set via ldflags.
	Branch string
	// BuildUser is the user who built the binary, set via ldflags.
	BuildUser string
	// BuildDate is when the binary was built. When empty at startup, init
	// fills it from the vcs.time build setting stamped by the Go toolchain.
	// It remains empty when neither ldflags nor VCS stamps are present (e.g.
	// module-proxy builds).
	BuildDate string

	// Revision is the git commit revision, derived from VCS build settings.
	Revision = getRevision()
	// GoVersion is the Go version used to build.
	GoVersion = runtime.Version()
	// GoOS is the operating system target.
	GoOS = runtime.GOOS
	// GoArch is the architecture target.
	GoArch = runtime.GOARCH
)

func init() {
	if BuildDate != "" {
		return
	}

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	for _, v := range buildInfo.Settings {
		if v.Key == "vcs.time" {
			BuildDate = v.Value
			return
		}
	}
}

// GetVersion returns a non-empty display string for the binary version.
//
// The fallback chain is:
//  1. [Version] verbatim when set via ldflags.
//  2. The main module version from [debug.ReadBuildInfo] when non-empty and
//     not "(devel)"; covers binaries installed with "go install module@vX.Y.Z".
//  3. "devel-" + [Revision] (e.g. "devel-abc1234-dirty") when Revision is
//     not "unknown".
//  4. "devel" as the final fallback.
//
// Use [Release] when you need to distinguish a tagged release from a dev build.
func GetVersion() string {
	if Version != "" {
		return Version
	}

	buildInfo, ok := debug.ReadBuildInfo()
	if ok && buildInfo.Main.Version != "" && buildInfo.Main.Version != "(devel)" {
		return buildInfo.Main.Version
	}

	if Revision != revisionUnknown {
		return "devel-" + Revision
	}

	return "devel"
}

// Release returns [Version] and true when the binary was built as a tagged
// release (Version was injected via ldflags). It returns "" and false for
// development builds. Use [GetVersion] for display strings that fall back to
// the VCS revision.
func Release() (string, bool) { return Version, Version != "" }

// Info holds a snapshot of version and build metadata.
type Info struct {
	Version   string
	Revision  string
	Branch    string
	BuildUser string
	BuildDate string
	GoVersion string
	GoOS      string
	GoArch    string
}

// Get returns an Info snapshotting the package-level variables verbatim
// (raw Version, not the GetVersion fallback, so callers can distinguish
// unset from set).
func Get() Info {
	return Info{
		Version:   Version,
		Revision:  Revision,
		Branch:    Branch,
		BuildUser: BuildUser,
		BuildDate: BuildDate,
		GoVersion: GoVersion,
		GoOS:      GoOS,
		GoArch:    GoArch,
	}
}

// String renders a human-readable multi-line summary of the build info.
//
// The first line is "version " followed by Version, or Revision when Version
// is empty (mirroring GetVersion's display intent). Subsequent indented lines
// render revision, branch, build user, build date, and Go runtime details.
// Lines whose value is empty are omitted so dev builds render cleanly. There
// is no trailing newline.
func (i Info) String() string {
	display := i.Version
	if display == "" {
		display = i.Revision
	}

	var sb strings.Builder

	fmt.Fprintf(&sb, "version %s", display)

	if i.Revision != "" {
		fmt.Fprintf(&sb, "\n  revision: %s", i.Revision)
	}

	if i.Branch != "" {
		fmt.Fprintf(&sb, "\n  branch: %s", i.Branch)
	}

	if i.BuildUser != "" {
		fmt.Fprintf(&sb, "\n  build user: %s", i.BuildUser)
	}

	if i.BuildDate != "" {
		fmt.Fprintf(&sb, "\n  build date: %s", i.BuildDate)
	}

	fmt.Fprintf(&sb, "\n  go: %s %s/%s", i.GoVersion, i.GoOS, i.GoArch)

	return sb.String()
}

func getRevision() string {
	rev := revisionUnknown

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return rev
	}

	modified := false

	for _, v := range buildInfo.Settings {
		switch v.Key {
		case "vcs.revision":
			if len(v.Value) > 7 {
				rev = v.Value[:7]
			} else {
				rev = v.Value
			}

		case "vcs.modified":
			if v.Value == "true" {
				modified = true
			}
		}
	}

	if modified {
		return rev + "-dirty"
	}

	return rev
}
