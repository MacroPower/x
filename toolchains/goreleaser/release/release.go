// Package release holds pure helpers for release tag and digest math. It is
// deliberately free of Dagger imports so the logic can be unit-tested with a
// plain `go test`, unlike the main module package whose generated client
// requires a live engine session. The module's Dagger Functions thin-delegate
// here.
package release

import (
	"fmt"
	"strings"
)

// IsPrerelease reports whether the version tag contains a pre-release
// identifier (e.g. "v1.0.0-rc.1"). Per SemVer the pre-release is the
// hyphen-introduced segment before any "+" build metadata, so build metadata
// is stripped first: it may itself contain hyphens (e.g. "v1.0.0+21AF---117B")
// and must not be mistaken for a pre-release marker.
func IsPrerelease(tag string) bool {
	v := strings.TrimPrefix(tag, "v")
	if plus := strings.IndexByte(v, '+'); plus >= 0 {
		v = v[:plus]
	}
	return strings.Contains(v, "-")
}

// VersionTags returns the image tags derived from a version tag string. For
// example, "v1.2.3" yields ["latest", "v1.2.3", "v1", "v1.2"]. Pre-release
// versions (e.g. "v1.0.0-rc.1") yield only the exact tag.
func VersionTags(tag string) []string {
	if IsPrerelease(tag) {
		return []string{tag}
	}

	v := strings.TrimPrefix(tag, "v")
	parts := strings.SplitN(v, ".", 3)

	candidates := []string{"latest", tag}
	if len(parts) >= 1 {
		candidates = append(candidates, "v"+parts[0])
	}
	if len(parts) >= 2 {
		candidates = append(candidates, "v"+parts[0]+"."+parts[1])
	}

	seen := make(map[string]bool, len(candidates))
	tags := make([]string, 0, len(candidates))
	for _, t := range candidates {
		if seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	return tags
}

// DeduplicateDigests returns unique image references from a list, keeping only
// the first occurrence of each sha256 digest. References without a digest are
// dropped.
func DeduplicateDigests(refs []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, ref := range refs {
		parts := strings.SplitN(ref, "@sha256:", 2)
		if len(parts) != 2 {
			continue
		}
		if !seen[parts[1]] {
			seen[parts[1]] = true
			unique = append(unique, ref)
		}
	}
	return unique
}

// FormatDigestChecksums converts publish output references to the checksums
// format expected by actions/attest-build-provenance. Each reference has the
// form "registry/image:tag@sha256:hex"; this emits "hex  registry/image:tag"
// lines, deduplicating by digest.
func FormatDigestChecksums(refs []string) string {
	var b strings.Builder
	for _, ref := range DeduplicateDigests(refs) {
		parts := strings.SplitN(ref, "@sha256:", 2)
		if len(parts) != 2 {
			continue
		}
		fmt.Fprintf(&b, "%s  %s\n", parts[1], parts[0])
	}
	return b.String()
}

// RegistryHost extracts the host (with optional port) from a registry address.
// For example, "ghcr.io/macropower/eidetic" returns "ghcr.io".
func RegistryHost(registry string) string {
	return strings.SplitN(registry, "/", 2)[0]
}

// platformToFileArch maps a Go platform architecture (GOARCH) to the
// architecture token printed by the `file` command.
var platformToFileArch = map[string]string{
	"amd64": "x86-64",
	"arm64": "aarch64",
}

// FileArch returns the architecture token that `file` prints for the given Go
// platform string. It accepts a bare GOARCH ("amd64") or a full platform
// ("linux/amd64") and uses the GOARCH component. An unrecognized architecture
// returns an error.
func FileArch(platform string) (string, error) {
	parts := strings.Split(platform, "/")
	arch := parts[len(parts)-1]
	if len(parts) >= 2 {
		arch = parts[1]
	}
	expected, ok := platformToFileArch[arch]
	if !ok {
		return "", fmt.Errorf("unknown platform architecture %q", arch)
	}
	return expected, nil
}
