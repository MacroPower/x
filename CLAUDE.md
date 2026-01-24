# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
task format # Format and lint
task lint   # Lint only
task test   # Run all tests
```

## Architecture

```bash
task docs # Print all package docs
```

## Code Style

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
