package refresolve

import "errors"

var (
	// ErrNotResolved is returned by a resolver to report a URI it does not
	// serve. It follows [io/fs.ErrNotExist]: answer with the sentinel (or an
	// error wrapping it) to decline, and match it with [errors.Is]. The parent
	// package re-exports it as its public ErrNotResolved, so the identity is
	// shared across the boundary.
	ErrNotResolved = errors.New("schema URI not resolved")

	// ErrRefResolve is returned when resolving a remote $ref URI fails. The
	// parent package re-exports it as its public ErrRefResolve, so [errors.Is]
	// matches a resolution failure through either name.
	ErrRefResolve = errors.New("ref resolve")
)
