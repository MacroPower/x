// Integration tests for the [Commitlint] module. Individual tests are
// annotated with +check so `dagger check -m toolchains/commitlint/tests`
// runs them all concurrently.

package main

import (
	"context"
	"fmt"

	"dagger/tests/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// Tests provides integration tests for the [Commitlint] module. Create
// instances with [New].
type Tests struct{}

// All runs all tests in parallel.
func (m *Tests) All(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error { return m.TestLintValid(ctx) })
	g.Go(func() error { return m.TestLintInvalid(ctx) })

	return g.Wait()
}

// TestLintValid verifies that [Commitlint.Lint] accepts a valid
// conventional commit message.
//
// +check
func (m *Tests) TestLintValid(ctx context.Context) error {
	config := testConfig()

	msgFile := dag.Directory().
		WithNewFile("COMMIT_EDITMSG", "feat(cli): add new flag for output format\n").
		File("COMMIT_EDITMSG")

	return dag.Commitlint().Lint(ctx, config, dagger.CommitlintLintOpts{
		MsgFile: msgFile,
	})
}

// TestLintInvalid verifies that [Commitlint.Lint] rejects an invalid
// commit message.
//
// +check
func (m *Tests) TestLintInvalid(ctx context.Context) error {
	config := testConfig()

	msgFile := dag.Directory().
		WithNewFile("COMMIT_EDITMSG", "This is not a conventional commit.\n").
		File("COMMIT_EDITMSG")

	err := dag.Commitlint().Lint(ctx, config, dagger.CommitlintLintOpts{
		MsgFile: msgFile,
	})
	if err == nil {
		return fmt.Errorf("invalid commit message was not rejected")
	}
	return nil
}

// testConfig returns a synthetic project directory with a commitlint config.
// commitlint's --edit mode locates the repo root by searching for a .git
// entry, so a static .git/HEAD is injected (no real repository is needed).
func testConfig() *dagger.Directory {
	return dag.Directory().
		WithNewFile(".commitlintrc.yaml", `extends:
  - "@commitlint/config-conventional"
`).
		WithNewFile(".git/HEAD", "ref: refs/heads/main\n")
}
