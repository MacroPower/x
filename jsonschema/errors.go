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

	// ErrInvalidType is returned by [CheckTypeNames] and by [Compile] (and
	// the one-shot [Validate] / [ValidateJSON] helpers), which routes through
	// the same check, when a schema's type keyword names something other than
	// the seven JSON Schema type names ("null", "boolean", "string",
	// "integer", "number", "object", "array"). A typo'd type would otherwise
	// compile cleanly and then reject every instance at runtime.
	ErrInvalidType = errors.New("invalid type name")

	// ErrInvalidSchemaDocument is returned by [CompileJSON] and
	// [SchemaFromValue] when a schema document's top-level value is not a
	// JSON object or boolean.
	ErrInvalidSchemaDocument = errors.New("schema document must be a JSON object or boolean")

	// ErrUnknownVocabulary is returned when the resolved $vocabulary set is
	// unsatisfiable: it marks true a vocabulary that this implementation does
	// not recognize, or it includes the 2020-12 core vocabulary without marking
	// it required (which the spec does not permit).
	ErrUnknownVocabulary = errors.New("unknown required vocabulary")

	// ErrRefResolve is returned when a [RefResolver] returns an error while
	// resolving a remote $ref URI. [Inline] also wraps it for a non-local ref
	// with no resolver configured and for any ref whose target cannot be
	// found.
	ErrRefResolve = errors.New("ref resolve")

	// ErrRefCycle is returned by [Inline] when expanding a $ref reaches a
	// schema whose own expansion is still in progress: the reference graph
	// is cyclic, so it has no finite static expansion.
	ErrRefCycle = errors.New("reference cycle")

	// ErrRefInline is returned by [Inline] for a reference construct with no
	// faithful static expansion. $dynamicRef resolves through the dynamic
	// scope at validation time, so no single replacement preserves its
	// semantics.
	ErrRefInline = errors.New("cannot inline reference")

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

	// The typed form of InstancePath, captured during the validation walk;
	// see [ValidationError.InstanceSegments].
	segments []Segment

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

// Segment is one step of an instance location: an object member key or an
// array index.
type Segment struct {
	// Key is the object property name. Meaningful only when IsIndex is false.
	Key string

	// Index is the array index. Meaningful only when IsIndex is true.
	Index int

	// IsIndex reports whether the segment addresses an array element rather
	// than an object property; it distinguishes array index 1 from the
	// property name "1", which the InstancePath JSON Pointer cannot.
	IsIndex bool
}

// InstanceSegments returns the typed path to the failing location in the
// input data, one Segment per reference token of [ValidationError.InstancePath],
// outermost first. Unlike re-parsing InstancePath, it distinguishes an array
// index from an object key that happens to look numeric. It is populated for
// errors produced by [Validate], [ValidateJSON], and the [Validator] methods;
// hand-constructed errors return nil.
func (e *ValidationError) InstanceSegments() []Segment {
	return e.segments
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

// Leaves returns the concrete, user-facing failures in the error tree, with the
// compositional and synthetic wrapper nodes that merely group them flattened
// away.
//
// Compositional keywords (allOf, anyOf, oneOf, if/then/else, $ref, $dynamicRef,
// the unevaluated* keywords) and the synthetic root that groups multiple
// top-level failures hold their real failures in [ValidationError.Causes], so
// Leaves descends through them. A childless node is a leaf, as is a
// propertyNames error: it carries the inner name-check failure as a cause but is
// itself the concrete failure, naming and locating the offending key. Each leaf
// is returned once, even when it is shared or reached through a cycle.
//
// Leaves suits reporting that wants one entry per distinct failure rather than
// the full nested tree. The receiver is included when it is itself a leaf, so a
// single-failure error returns a one-element slice.
func (e *ValidationError) Leaves() []*ValidationError {
	var leaves []*ValidationError

	e.collectLeaves(&leaves, map[*ValidationError]bool{})

	return leaves
}

// collectLeaves appends the leaf failures reachable from e to out. The seen set
// guards against cycles and shared nodes so each leaf is collected once.
func (e *ValidationError) collectLeaves(out *[]*ValidationError, seen map[*ValidationError]bool) {
	if e == nil || seen[e] {
		return
	}

	seen[e] = true

	if e.isLeaf() {
		*out = append(*out, e)

		return
	}

	for _, cause := range e.Causes {
		cause.collectLeaves(out, seen)
	}
}

// isLeaf reports whether e is a concrete failure to report rather than a wrapper
// to descend into. A node with no causes is always a leaf; a propertyNames node
// is a leaf despite its cause, because it is the concrete, self-describing
// failure for an offending key.
func (e *ValidationError) isLeaf() bool {
	return len(e.Causes) == 0 || e.Keyword == keywordPropertyNames
}

// TargetsKey reports whether the failing keyword constrains a member's key or an
// object's or array's structure — its presence, name, size, or membership —
// rather than the content of a value.
//
// It is true for additionalProperties, propertyNames, required, the object size
// keywords (minProperties, maxProperties), and the array size and membership
// keywords (minItems, maxItems, uniqueItems, contains, minContains,
// maxContains). A source-mapping consumer can use it to decide whether to
// highlight a key (or the containing key) instead of a value when rendering a
// failure against the original input document.
func (e *ValidationError) TargetsKey() bool {
	switch e.Keyword {
	case keywordAdditionalProperties, keywordPropertyNames, keywordRequired,
		keywordMinProperties, keywordMaxProperties,
		keywordMinItems, keywordMaxItems, keywordUniqueItems,
		keywordContains, keywordMinContains, keywordMaxContains:
		return true
	default:
		return false
	}
}
