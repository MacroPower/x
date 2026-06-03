package release

import (
	"reflect"
	"testing"
)

func TestIsPrerelease(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		tag  string
		want bool
	}{
		"stable":               {"v1.2.3", false},
		"stable no v":          {"1.2.3", false},
		"prerelease rc":        {"v1.0.0-rc.1", true},
		"prerelease beta":      {"v2.0.0-beta.1", true},
		"build metadata":       {"v1.0.0+21AF26D3", false},
		"build with hyphen":    {"v1.0.0+build-1", false},
		"build hyphens galore": {"v1.0.0+21AF26D3---117B344092BD", false},
		"prerelease and build": {"v1.0.0-rc.1+build.5", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := IsPrerelease(tc.tag); got != tc.want {
				t.Errorf("IsPrerelease(%q) = %v, want %v", tc.tag, got, tc.want)
			}
		})
	}
}

func TestVersionTags(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		tag  string
		want []string
	}{
		"stable":            {"v1.2.3", []string{"latest", "v1.2.3", "v1", "v1.2"}},
		"stable zero patch": {"v2.0.0", []string{"latest", "v2.0.0", "v2", "v2.0"}},
		"prerelease":        {"v1.0.0-rc.1", []string{"v1.0.0-rc.1"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := VersionTags(tc.tag); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("VersionTags(%q) = %v, want %v", tc.tag, got, tc.want)
			}
		})
	}
}

func TestDeduplicateDigests(t *testing.T) {
	t.Parallel()
	refs := []string{
		"ghcr.io/x:latest@sha256:aaa",
		"ghcr.io/x:v1@sha256:aaa", // duplicate digest of latest
		"ghcr.io/x:v1.2@sha256:bbb",
		"invalid-no-digest", // dropped
	}
	want := []string{"ghcr.io/x:latest@sha256:aaa", "ghcr.io/x:v1.2@sha256:bbb"}
	if got := DeduplicateDigests(refs); !reflect.DeepEqual(got, want) {
		t.Errorf("DeduplicateDigests = %v, want %v", got, want)
	}
}

func TestFormatDigestChecksums(t *testing.T) {
	t.Parallel()
	refs := []string{
		"ghcr.io/x:latest@sha256:aaa",
		"ghcr.io/x:v1@sha256:aaa", // collapsed
		"ghcr.io/x:v1.2@sha256:bbb",
	}
	want := "aaa  ghcr.io/x:latest\nbbb  ghcr.io/x:v1.2\n"
	if got := FormatDigestChecksums(refs); got != want {
		t.Errorf("FormatDigestChecksums = %q, want %q", got, want)
	}
}

func TestRegistryHost(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"ghcr.io/macropower/eidetic": "ghcr.io",
		"localhost:5000/x":           "localhost:5000",
		"ghcr.io":                    "ghcr.io",
	}
	for in, want := range cases {
		if got := RegistryHost(in); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileArch(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		platform string
		want     string
		err      bool
	}{
		"linux amd64": {"linux/amd64", "x86-64", false},
		"linux arm64": {"linux/arm64", "aarch64", false},
		"bare amd64":  {"amd64", "x86-64", false},
		"unknown":     {"linux/riscv64", "", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := FileArch(tc.platform)
			if tc.err {
				if err == nil {
					t.Errorf("FileArch(%q) expected error, got %q", tc.platform, got)
				}
				return
			}
			if err != nil {
				t.Errorf("FileArch(%q) unexpected error: %v", tc.platform, err)
			}
			if got != tc.want {
				t.Errorf("FileArch(%q) = %q, want %q", tc.platform, got, tc.want)
			}
		})
	}
}
