package jsonschema

import (
	"errors"
	"strings"
)

var (
	// ErrUnsupportedType is returned when a Go type has no JSON Schema
	// representation (e.g., func, chan, complex, [unsafe.Pointer]).
	ErrUnsupportedType = errors.New("unsupported type")

	// ErrUnsupportedMapKey is returned when a map key type is neither
	// string, an integer type, nor an [encoding.TextMarshaler].
	ErrUnsupportedMapKey = errors.New("unsupported map key type")

	// ErrUnknownVocabulary is returned when the resolved $vocabulary set is
	// unsatisfiable: it marks true a vocabulary that this implementation does
	// not recognize, or it includes the 2020-12 core vocabulary without marking
	// it required (which the spec does not permit).
	ErrUnknownVocabulary = errors.New("unknown required vocabulary")

	// ErrRefResolve is returned when a [RefResolver] returns an error while
	// resolving a remote $ref URI.
	ErrRefResolve = errors.New("ref resolve")

	// ErrProviderPanic is returned when a user-supplied JSONSchemaProvider or
	// JSONSchemaExtender method panics during generation (for example by
	// dereferencing a nil pointer field on the zero value it is invoked
	// against). The panic is recovered so Generate returns this error instead
	// of crashing.
	ErrProviderPanic = errors.New("provider panicked")
)

// ValidationError represents a JSON Schema validation failure.
//
// The error carries the instance path (JSON Pointer into the input data),
// the schema path (JSON Pointer into the schema), the keyword that triggered
// the failure, and a human-readable message. Compositional and container
// keywords populate [ValidationError.Causes] with child errors forming a tree
// that mirrors the schema/instance structure.
//
// The returned error from [Validate] and [ValidateJSON] can be unwrapped
// to *ValidationError via [errors.As].
type ValidationError struct {
	// Optional wrapped error (e.g. a [RefResolver] failure wrapping
	// [ErrRefResolve]) that [errors.Is] and [errors.As] can match.
	err error

	// InstancePath is the JSON Pointer path to the failing location in the
	// input data (e.g., "/address/city").
	InstancePath string

	// SchemaPath is the JSON Pointer path to the keyword that triggered the
	// failure within the schema
	// (e.g., "/properties/address/properties/city/minLength").
	SchemaPath string

	// Keyword is the JSON Schema keyword that failed (e.g., "type", "required",
	// "minLength", "pattern").
	Keyword string

	// Message is a human-readable description of the failure.
	Message string

	// Causes contains child ValidationError entries. Compositional keywords
	// (allOf, anyOf, oneOf, if/then/else, $ref, $dynamicRef, and the
	// unevaluated* keywords) wrap their child failures in an intermediate node.
	// Container keywords (properties, items, additionalProperties, and the like)
	// instead flatten their child failures directly into the parent's Causes,
	// each child retaining its full instance and schema path. The not keyword
	// produces a childless leaf error: it fails precisely when its subschema
	// succeeds, so there are no child failures to wrap.
	Causes []*ValidationError
}

// Error returns a multi-line string representation. The top-level message is
// on the first line; each Causes entry is indented and rendered recursively.
// For a single-error case the output is one line.
func (e *ValidationError) Error() string {
	var b strings.Builder

	e.writeError(&b, 0, map[*ValidationError]bool{})

	return b.String()
}

// writeError recursively writes the error tree with indentation. The seen set
// guards against cycles in the cause graph so a malformed (cyclic) tree does
// not recurse without bound.
func (e *ValidationError) writeError(b *strings.Builder, depth int, seen map[*ValidationError]bool) {
	if seen[e] {
		return
	}

	seen[e] = true

	indent := strings.Repeat("  ", depth)

	hasHeader := e.InstancePath != "" || e.Keyword != "" || e.Message != ""

	//nolint:nestif // Rendering nested error tree requires conditional nesting.
	if hasHeader {
		b.WriteString(indent)

		if e.InstancePath != "" {
			b.WriteString(e.InstancePath)
		}

		if e.Keyword != "" {
			if e.InstancePath != "" {
				b.WriteString(" ")
			}

			b.WriteString("(")
			b.WriteString(e.Keyword)
			b.WriteString(")")
		}

		if e.Message != "" {
			if e.InstancePath != "" || e.Keyword != "" {
				b.WriteString(": ")
			}

			b.WriteString(e.Message)
		}
	}

	// A header-less node (such as the synthetic root wrapping multiple
	// top-level errors) prints no line of its own, so its children render at
	// the current depth rather than depth+1; this keeps indentation tracking
	// the visible tree depth instead of skipping a level.
	childDepth := depth
	if hasHeader {
		childDepth = depth + 1
	}

	// Wrote tracks whether the header line or an earlier sibling already emitted
	// output, so a separating newline is inserted only between non-empty renders.
	// A cause already in seen (a shared or cyclic node) renders nothing, so
	// skipping it here avoids leaving a stray blank line behind it.
	wrote := hasHeader
	for _, cause := range e.Causes {
		if seen[cause] {
			continue
		}

		if wrote {
			b.WriteString("\n")
		}

		cause.writeError(b, childDepth, seen)

		wrote = true
	}
}

// Unwrap returns the underlying errors so [errors.Is] and [errors.As] can
// traverse the whole error tree. It yields every directly attached error (for
// example a [RefResolver] failure wrapping [ErrRefResolve]) found anywhere in
// the cause tree, so a sentinel attached at any depth or position is reachable.
//
// Rather than returning each child *ValidationError for the standard library to
// recurse into, Unwrap flattens the reachable cause graph itself under a visited
// guard, mirroring the cycle protection in [ValidationError.Error]. A malformed
// (cyclic) or shared-node cause graph therefore cannot make [errors.Is] or
// [errors.As] loop without bound or revisit a node exponentially.
func (e *ValidationError) Unwrap() []error {
	var errs []error

	e.collectAttached(&errs, map[*ValidationError]bool{})

	return errs
}

// collectAttached appends every distinct attached err in the cause graph rooted
// at e to errs. The seen set guards against cycles and shared nodes so the
// traversal visits each node at most once.
func (e *ValidationError) collectAttached(errs *[]error, seen map[*ValidationError]bool) {
	if e == nil || seen[e] {
		return
	}

	seen[e] = true

	if e.err != nil {
		*errs = append(*errs, e.err)
	}

	for _, cause := range e.Causes {
		cause.collectAttached(errs, seen)
	}
}
