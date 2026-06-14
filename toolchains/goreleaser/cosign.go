// Sigstore cosign signing, folded into the goreleaser toolchain because it is
// part of the same release pipeline: WithCosign installs the cosign binary into
// a release container (where GoReleaser's sign step drives it for blob
// signing), and SignKeyless signs published image digests directly (Fulcio +
// Rekor, via an OIDC token). Digests are signed concurrently; callers
// deduplicate digests before signing, since multiple tags often share one
// manifest.
package main

import (
	"context"
	"errors"
	"fmt"

	"dagger/goreleaser/internal/dagger"

	"golang.org/x/sync/errgroup"
)

const (
	// cosignVersion is the pinned cosign release used for the default image.
	cosignVersion = "v3.1.1" // renovate: datasource=github-releases depName=sigstore/cosign

	// cosignImage is the official cosign image the binary is extracted from.
	cosignImage = "gcr.io/projectsigstore/cosign:" + cosignVersion

	// cosignBinPath is the cosign executable path inside the official image.
	cosignBinPath = "/ko-app/cosign"

	// maxParallelSigns bounds the number of concurrent cosign invocations so a
	// large multi-arch, multi-image release does not burst unbounded.
	maxParallelSigns = 8
)

// ErrRegistryHostRequired indicates a registry password was supplied without a
// registry host. cosign keys its auth entry on the host, so an empty host
// would silently produce an unusable entry and fall back to anonymous access.
var ErrRegistryHostRequired = errors.New("registry host is required when a registry password is set")

// cosignBinary returns the cosign executable, extracted from the official image
// so it can be layered onto a release container (where GoReleaser drives its
// own blob signing).
func (m *Goreleaser) cosignBinary() *dagger.File {
	return dag.Container().From(cosignImage).File(cosignBinPath)
}

// WithCosign installs the cosign binary at /usr/local/bin/cosign in the given
// container, for tools (like GoReleaser's sign step) that invoke it directly.
func (m *Goreleaser) WithCosign(
	// Container to install cosign into.
	ctr *dagger.Container,
) *dagger.Container {
	return ctr.WithFile("/usr/local/bin/cosign", m.cosignBinary())
}

// SignKeyless signs each digest using cosign keyless signing (Fulcio + Rekor).
// Cosign's built-in GitHub Actions provider uses the OIDC request URL and token
// to fetch fresh tokens on demand, avoiding expiry issues. When registry
// credentials are supplied, a Docker config is mounted so cosign can push
// signatures to a private registry (cosign makes its own HTTP requests, which
// Dagger's registry auth does not cover).
func (m *Goreleaser) SignKeyless(
	ctx context.Context,
	// Image digests to sign (e.g. "registry/image:tag@sha256:hex"). Caller
	// should deduplicate by digest first.
	digests []string,
	// OIDC token request URL (GitHub Actions: ACTIONS_ID_TOKEN_REQUEST_URL).
	oidcRequestURL string,
	// Bearer token for the OIDC request (GitHub Actions:
	// ACTIONS_ID_TOKEN_REQUEST_TOKEN).
	oidcRequestToken *dagger.Secret,
	// Registry host for cosign auth (e.g. "ghcr.io"). Required with a password.
	// +optional
	registryHost string,
	// Registry username for cosign auth.
	// +optional
	registryUsername string,
	// Registry password/token for cosign auth. When set, a Docker config is
	// mounted for cosign's own registry requests.
	// +optional
	registryPassword *dagger.Secret,
) error {
	if len(digests) == 0 {
		return nil
	}
	if registryPassword != nil && registryHost == "" {
		return ErrRegistryHostRequired
	}
	ctr := m.cosignBase(registryHost, registryUsername, registryPassword).
		WithEnvVariable("ACTIONS_ID_TOKEN_REQUEST_URL", oidcRequestURL).
		WithSecretVariable("ACTIONS_ID_TOKEN_REQUEST_TOKEN", oidcRequestToken)
	return m.signAll(ctx, digests, ctr, func(digest string) []string {
		return []string{"cosign", "sign", digest, "--yes"}
	})
}

// cosignBase returns the cosign container, optionally mounting a Docker config
// so cosign can authenticate its own HTTP requests to a private registry.
func (m *Goreleaser) cosignBase(registryHost, registryUsername string, registryPassword *dagger.Secret) *dagger.Container {
	ctr := dag.Container().From(cosignImage)
	if registryPassword != nil {
		cfg := dockerConfigFile(registryHost, registryUsername, registryPassword)
		ctr = ctr.
			WithMountedFile("/tmp/docker/config.json", cfg).
			WithEnvVariable("DOCKER_CONFIG", "/tmp/docker")
	}
	return ctr
}

// signAll runs `cosign sign` for every digest concurrently using args(digest)
// as the command, and reports the first failure.
func (m *Goreleaser) signAll(
	ctx context.Context,
	digests []string,
	ctr *dagger.Container,
	args func(digest string) []string,
) error {
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(maxParallelSigns)
	for _, digest := range digests {
		g.Go(func() error {
			_, err := ctr.WithExec(args(digest)).Sync(gCtx)
			if err != nil {
				return fmt.Errorf("sign image %s: %w", digest, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// dockerConfigFile generates a Docker config.json containing registry
// credentials, built in a helper container so the password stays a
// [dagger.Secret] throughout.
func dockerConfigFile(host, username string, password *dagger.Secret) *dagger.File {
	return dag.Container().
		From(debianImage).
		WithEnvVariable("REG_HOST", host).
		WithEnvVariable("REG_USER", username).
		WithSecretVariable("REG_PASS", password).
		WithExec([]string{"sh", "-c",
			`printf '{"auths":{"%s":{"auth":"%s"}}}' "$REG_HOST" "$(printf '%s:%s' "$REG_USER" "$REG_PASS" | base64 -w0)" > /tmp/config.json`,
		}).
		File("/tmp/config.json")
}
