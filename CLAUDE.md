# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
task format # Format and tidy code, run generators
task lint   # golangci-lint, go mod tidy check, prettier
task test   # Run all tests (unit + integration)
task check  # Everything CI runs (lint + test + security + GitHub config + releaser checks)
```

Devbox provides all required tools on PATH automatically.

CI runs these same tasks inside the devbox environment via the `ci` Dagger
toolchain (`dagger call ci <task>`), so local and CI execute identical commands.

## Architecture

Monorepo. Packages have their own CLAUDE.md file with more information.

## Code Style

All packages follow similar conventions.

### Go Conventions

- Document all exported items with doc comments.
- Wrap errors with `fmt.Errorf("context: %w", err)`, or `fmt.Errorf("%w: %w", ErrSentinel, err)`.
- Avoid using "failed" or "error" in library error messages.
- Use global error variables for common errors.
- Use constructors with functional options.
- Accept interfaces, return concrete types.

### Testing

- Use `github.com/stretchr/testify/assert` and `require`.
- Table-driven tests with `map[string]struct{}` format.
- Field names: prefer `want` for expected output, `err` for expected errors.
- For inputs, use clear contextual names (e.g., `before`/`after` for diffs, `line`/`col` for positions).
- Always use `t.Parallel()` in all tests.
- Use `require.ErrorIs` for sentinel error checking.
- Use `require.ErrorAs` for error type extraction.
- Use the `stringtest` helpers for all multi-line strings.
