// Package version provides build-time version information for binaries.
//
// # Wiring
//
// The canonical goreleaser snippet is a single ldflag:
//
//	ldflags:
//	  - -X go.jacobcolvin.com/x/version.Version={{.Version}}
//
// Branch, BuildUser, and BuildDate are optional and remain empty when unset.
// Revision and BuildDate fall back to VCS build settings stamped by the Go
// toolchain for plain "go build" from a clone, so development binaries report
// meaningful information without any ldflags.
//
// Module-proxy builds (goreleaser gomod.proxy, "go install pkg@version") carry
// no VCS stamps, so proxy-built releases that want a build date should pass
//
//	-X go.jacobcolvin.com/x/version.BuildDate={{.Date}}
//
// which needs no environment variables.
package version
