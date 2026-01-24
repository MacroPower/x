package version

import (
	"runtime"
	"runtime/debug"
)

var (
	// Version is the application version, set via ldflags.
	Version string
	// Branch is the git branch, set via ldflags.
	Branch string
	// BuildUser is the user who built the binary, set via ldflags.
	BuildUser string
	// BuildDate is when the binary was built, set via ldflags.
	BuildDate string

	// Revision is the git commit revision.
	Revision = getRevision()
	// GoVersion is the Go version used to build.
	GoVersion = runtime.Version()
	// GoOS is the operating system target.
	GoOS = runtime.GOOS
	// GoArch is the architecture target.
	GoArch = runtime.GOARCH
)

func getRevision() string {
	rev := "unknown"

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return rev
	}

	modified := false

	for _, v := range buildInfo.Settings {
		switch v.Key {
		case "vcs.revision":
			rev = v.Value
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
