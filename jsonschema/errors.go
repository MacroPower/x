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

	// ErrInvalidType is returned when a schema's type keyword names something
	// other than the seven JSON Schema type names ("null", "boolean", "string",
	// "integer", "number", "object", "array"). It is reported by
	// [CheckTypeNames] and by [Compile], as well as by the one-shot [Validate]
	// helper, which routes through the same check. A typo'd type would otherwise
	// compile cleanly and then reject every instance at runtime.
	ErrInvalidType = errors.New("invalid type name")

	// ErrItemsArrayUnderDraft2020 is returned by [Compile] when a schema
	// compiled under [Draft2020] sets the array form of the items keyword
	// (the field upstream parses a JSON `"items": [ ... ]` into). Array-form
	// items is the Draft-7 spelling of tuple validation; under 2020-12 tuples
	// are spelled with prefixItems and array-form items has no meaning, so the
	// 2020-12 walk would silently validate every element against nothing.
	// Rejecting it at construction surfaces the dropped constraint instead of
	// accepting every instance; set the Draft-7 $schema (or [WithDraft]) for
	// tuple semantics, or use prefixItems.
	ErrItemsArrayUnderDraft2020 = errors.New("array-form items is not valid under draft 2020-12")

	// ErrNegativeBound is returned by [Compile] when a length or count keyword
	// (minLength, maxLength, minItems, maxItems, minProperties, maxProperties,
	// minContains, maxContains) carries a negative value, which the spec defines
	// as a non-negative integer. Schema.Resolve does not enforce it, so a
	// negative bound would otherwise compile cleanly and then silently
	// mis-validate: a negative maximum rejects every instance and a negative
	// minimum is a dead no-op. Rejecting it at construction surfaces the
	// malformed schema instead.
	ErrNegativeBound = errors.New("negative bound")

	// ErrInvalidSchemaDocument is returned by [CompileJSON], [ParseSchema],
	// and [ParseSchemaValue] when a schema document's top-level value is not a
	// JSON object or boolean.
	ErrInvalidSchemaDocument = errors.New("schema document must be a JSON object or boolean")

	// ErrNilSchema is returned by [Compile] (and the one-shot [Validate]
	// helper) when the schema argument is nil. A nil *Schema carries no draft,
	// vocabulary, or structure to compile; it is reported through the error
	// contract rather than dereferenced into a panic.
	ErrNilSchema = errors.New("nil schema")

	// ErrUnknownVocabulary is returned when the resolved $vocabulary set is
	// unsatisfiable: it marks true a vocabulary that this implementation does
	// not recognize, or it includes the 2020-12 core vocabulary without marking
	// it required (which the spec does not permit).
	ErrUnknownVocabulary = errors.New("unknown required vocabulary")

	// ErrNotResolved is returned by a [RefResolver] to report a URI it does
	// not serve. The not-resolved answer passes the URI along to the next
	// [ChainResolvers] link, and ultimately to the unresolvable-ref handling of
	// the entry point in effect. It follows [io/fs.ErrNotExist]: answer with the
	// sentinel (or an error wrapping it) to decline, and match it with
	// [errors.Is]. Any other error reports a resolution attempt that failed and
	// stops resolution.
	ErrNotResolved = errors.New("schema URI not resolved")

	// ErrTypeNotHandled is returned by a [TypeSchemaProvider] to report a Go
	// type it does not handle, passing resolution to the next provider and
	// then to the rest of the type resolution chain. It is the
	// [ErrNotResolved] of the generation side: answer with the sentinel (or
	// an error wrapping it) to decline, and match it with [errors.Is]. Any
	// other error aborts generation.
	ErrTypeNotHandled = errors.New("type not handled")

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

	// ErrInvalidDefaultsInstance is returned by [Generate] when the instance
	// given to [WithDefaultsFrom] is unusable: its pointer-dereferenced dynamic
	// type is not the pointer-dereferenced generated root type, it does not
	// marshal to a JSON object, or the generated root resolves to a bare $ref
	// with no properties to seed.
	ErrInvalidDefaultsInstance = errors.New("invalid defaults instance")
)

// ValidationError represents a JSON Schema validation failure.
//
// The error carries the instance path (JSON Pointer into the input data),
// the schema path (JSON Pointer into the schema), the keyword that triggered
// the failure, and a human-readable message. Compositional and container
// keywords populate [ValidationError.Causes] with child errors forming a tree
// that mirrors the schema/instance structure.
//
// The returned error from [Validate] and the [Validator] methods
// can be unwrapped to *ValidationError via [errors.AsType].
type ValidationError struct {
	// Optional wrapped error (e.g. a [RefResolver] failure wrapping
	// [ErrRefResolve]) that [errors.Is] and [errors.As] can match.
	err error

	// The typed form of InstancePath, captured during the validation walk;
	// see [ValidationError.InstanceSegments].
	segments []Segment

	// The typed form of SchemaPath, captured during the validation walk;
	// see [ValidationError.SchemaSegments].
	schemaSegs []Segment

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

// Segment is one step of a JSON Pointer location: an object member key or
// an array index. Validation errors carry instance locations as segments
// ([ValidationError.InstanceSegments]), and [Location.Segments]
// carries schema locations the same way.
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
// errors produced by [Validate] and the [Validator] methods;
// hand-constructed errors return nil.
func (e *ValidationError) InstanceSegments() []Segment {
	return e.segments
}

// SchemaSegments returns the typed path to the keyword that triggered the
// failure within the schema, one Segment per reference token of
// [ValidationError.SchemaPath], outermost first. It is the schema-side
// counterpart of [ValidationError.InstanceSegments] and mirrors
// [Location.Segments]. Unlike re-parsing SchemaPath, it carries member keys
// verbatim (no ~0/~1 escaping to undo) and distinguishes a list index (an allOf
// branch) from a property named like a number. It is populated for errors
// produced by [Validate] and the [Validator] methods; hand-constructed errors
// return nil.
func (e *ValidationError) SchemaSegments() []Segment {
	return e.schemaSegs
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
// holds every node already rendered: a node is added on entry and never removed,
// so a cyclic cause graph terminates and a node shared by disjoint branches (a
// DAG) renders once rather than once per reaching path. The validator only ever
// produces trees, so this affects output only for a caller-built shared-node
// graph, where it also avoids the O(2^depth) blowup a deep diamond would cost.
func (e *ValidationError) writeError(b *strings.Builder, depth int, seen map[*ValidationError]bool) {
	if e == nil || seen[e] {
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
	// Each cause renders into a scratch builder first: a cause that produces no
	// output (a nil entry, one already in seen, or a header-less node whose
	// descendants all render nothing) is skipped entirely and leaves no stray
	// separator before or after it.
	wrote := hasHeader
	for _, cause := range e.Causes {
		if seen[cause] {
			continue
		}

		var cb strings.Builder

		cause.writeError(&cb, childDepth, seen)

		if cb.Len() == 0 {
			continue
		}

		if wrote {
			b.WriteString("\n")
		}

		b.WriteString(cb.String())

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
	return len(e.Causes) == 0 || e.Keyword == KeywordPropertyNames
}

// TargetsKey reports whether the failing keyword constrains a member's key or an
// object's or array's structure rather than the content of a value. Structure
// here means presence, name, size, or membership.
//
// It is true for additionalProperties, propertyNames, required, the dependency
// keywords that report a missing property (dependentRequired and the legacy
// string-array form of dependencies), the object size keywords (minProperties,
// maxProperties), and the array size and membership keywords (minItems,
// maxItems, uniqueItems, contains, minContains, maxContains). A source-mapping
// consumer can use it to decide whether to highlight a key (or the containing
// key) instead of a value when rendering a failure against the original input
// document.
func (e *ValidationError) TargetsKey() bool {
	switch e.Keyword {
	case KeywordAdditionalProperties, KeywordPropertyNames, KeywordRequired,
		KeywordDependentRequired, KeywordDependencies,
		KeywordMinProperties, KeywordMaxProperties,
		KeywordMinItems, KeywordMaxItems, KeywordUniqueItems,
		KeywordContains, KeywordMinContains, KeywordMaxContains:
		return true
	default:
		return false
	}
}
