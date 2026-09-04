package refresolve

import (
	"errors"

	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
)

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

	// ErrIDCollision is returned when a document entering resolution space
	// claims an identifier another document already holds, either a $id
	// resolving to a registered URI or an anchor key registered for a different
	// schema. Two documents under one identifier leave every reference naming it
	// ambiguous, so the registration refuses the claim rather than picking a
	// winner. The value is [schemavet.ErrIDCollision], which reports the
	// within-document form of the same claim, and the parent package
	// re-exports it as its public ErrIDCollision, so [errors.Is] matches
	// through every name.
	ErrIDCollision = schemavet.ErrIDCollision
)
