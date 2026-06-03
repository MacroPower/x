// Cosign signs published container image digests with Sigstore cosign, either
// keyless (Fulcio + Rekor, via an OIDC token) or with a private key. Digests
// are signed concurrently. Callers deduplicate digests before signing, since
// multiple tags often share one manifest.
package main

import (
	"context"
	"errors"
	"fmt"

	"dagger/cosign/internal/dagger"

	"golang.org/x/sync/errgroup"
)

const (
	// cosignVersion is the pinned cosign release used for the default image.
	cosignVersion = "v3.0.4" // renovate: datasource=github-releases depName=sigstore/cosign

	defaultImage = "gcr.io/projectsigstore/cosign:" + cosignVersion

	// binPath is the cosign executable path inside the official image.
	binPath = "/ko-app/cosign"

	// maxParallelSigns bounds the number of concurrent cosign invocations so a
	// large multi-arch, multi-image release does not burst unbounded.
	maxParallelSigns = 8
)

// ErrRegistryHostRequired indicates a registry password was supplied without a
// registry host. cosign keys its auth entry on the host, so an empty host
// would silently produce an unusable entry and fall back to anonymous access.
var ErrRegistryHostRequired = errors.New("registry host is required when a registry password is set")

// Cosign signs container image digests with Sigstore cosign. Create instances
// with [New].
type Cosign struct {
	// cosign container image reference.
	Image string
}

// New creates a new [Cosign] module.
func New(
	// cosign container image.
	// +optional
	image string,
) *Cosign {
	if image == "" {
		image = defaultImage
	}
	return &Cosign{Image: image}
}

// Binary returns the cosign executable, extracted from the official image so it
// can be layered onto another container (e.g. a goreleaser release base, where
// goreleaser invokes cosign for blob signing).
func (m *Cosign) Binary() *dagger.File {
	return dag.Container().From(m.Image).File(binPath)
}

// WithCosign installs the cosign binary at /usr/local/bin/cosign in the given
// container, for tools (like goreleaser's sign step) that invoke it directly.
func (m *Cosign) WithCosign(
	// Container to install cosign into.
	ctr *dagger.Container,
) *dagger.Container {
	return ctr.WithFile("/usr/local/bin/cosign", m.Binary())
}

// SignKeyless signs each digest using cosign keyless signing (Fulcio + Rekor).
// Cosign's built-in GitHub Actions provider uses the OIDC request URL and token
// to fetch fresh tokens on demand, avoiding expiry issues. When registry
// credentials are supplied, a Docker config is mounted so cosign can push
// signatures to a private registry (cosign makes its own HTTP requests, which
// Dagger's registry auth does not cover).
func (m *Cosign) SignKeyless(
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
	ctr := m.base(registryHost, registryUsername, registryPassword).
		WithEnvVariable("ACTIONS_ID_TOKEN_REQUEST_URL", oidcRequestURL).
		WithSecretVariable("ACTIONS_ID_TOKEN_REQUEST_TOKEN", oidcRequestToken)
	return m.signAll(ctx, digests, ctr, func(digest string) []string {
		return []string{"cosign", "sign", digest, "--yes"}
	})
}

// SignWithKey signs each digest using a cosign private key (env://COSIGN_KEY).
// When registry credentials are supplied, a Docker config is mounted so cosign
// can push signatures to a private registry.
func (m *Cosign) SignWithKey(
	ctx context.Context,
	// Image digests to sign (e.g. "registry/image:tag@sha256:hex"). Caller
	// should deduplicate by digest first.
	digests []string,
	// cosign private key (the contents of a cosign.key file).
	key *dagger.Secret,
	// Password for the cosign private key, if it is encrypted.
	// +optional
	password *dagger.Secret,
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
	ctr := m.base(registryHost, registryUsername, registryPassword).
		WithSecretVariable("COSIGN_KEY", key)
	if password != nil {
		ctr = ctr.WithSecretVariable("COSIGN_PASSWORD", password)
	}
	return m.signAll(ctx, digests, ctr, func(digest string) []string {
		return []string{"cosign", "sign", "--key", "env://COSIGN_KEY", digest, "--yes"}
	})
}

// base returns the cosign container, optionally mounting a Docker config so
// cosign can authenticate its own HTTP requests to a private registry.
func (m *Cosign) base(registryHost, registryUsername string, registryPassword *dagger.Secret) *dagger.Container {
	ctr := dag.Container().From(m.Image)
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
func (m *Cosign) signAll(
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
		From("debian:13-slim").
		WithEnvVariable("REG_HOST", host).
		WithEnvVariable("REG_USER", username).
		WithSecretVariable("REG_PASS", password).
		WithExec([]string{"sh", "-c",
			`printf '{"auths":{"%s":{"auth":"%s"}}}' "$REG_HOST" "$(printf '%s:%s' "$REG_USER" "$REG_PASS" | base64 -w0)" > /tmp/config.json`,
		}).
		File("/tmp/config.json")
}
