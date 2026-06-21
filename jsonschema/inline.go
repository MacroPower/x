package jsonschema

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// inliner carries the configuration and per-call state of one [Inline] run:
// the functional options, a scratch validator holding the $id/$anchor
// registries for the pristine copy of the root document and of every fetched
// document, and the expansion bookkeeping that memoizes finished targets and
// detects reference cycles.
//
// Each document participates as a pristine copy that is never mutated: the
// registries are built over it and every ref-target resolution happens
// against it, while the output is assembled in separate working copies.
// Resolving against pristine structure keeps one ref's expansion from
// changing (or removing) what a later ref's JSON Pointer or anchor
// addresses.
type inliner struct {
	resolver RefResolver

	// The context of the [Inline] call, passed to the resolver with every
	// document fetch.
	ctx context.Context

	// The scratch validator resolving references. Its URI, anchor, and
	// base-URI registries are built by the same walk Compile uses, over the
	// pristine root copy and each pristine fetched-document copy, so
	// resolution matches the validator's and sees only original structure.
	v *validator

	// Pristine schemas whose self-contained copy is currently being built;
	// a ref that resolves to an in-flight schema is a cycle.
	inflight map[*Schema]bool

	// Pristine schemas mapped to their finished self-contained copies, so a
	// target referenced from several places is expanded once. Every
	// additional use clones the memoized copy, so no two positions in the
	// output share nodes.
	memo map[*Schema]*Schema

	// Pristine schemas mapped to their JSON Pointer path within their
	// containing document, recorded when each document joins resolution
	// space. The paths name ref-node locations for [WithRefFallback]
	// consultations and seed the path of each expansion walk.
	paths map[*Schema]string

	// Pristine schemas mapped to the URI of their containing document,
	// recorded alongside paths: the root document's $id or [WithBaseURI]
	// base ("" when it has neither), and each fetched document's $id or
	// retrieval URI. The URIs identify the failing document in
	// [WithRefFallback] consultations.
	docs map[*Schema]string

	// The per-reference failure policy from [WithRefFallback]; nil
	// means every expansion failure is fatal.
	fallback RefFallback

	// The WithDraft override; nil leaves the draft to $schema detection.
	draftOverride *Draft

	baseURI string

	// Current depth of nested substitute expansions. Each [SubstituteRef]
	// clone is a fresh schema the pointer-identity inflight guard never
	// matches, so a fallback that substitutes a schema with its own failing
	// ref would recurse without bound; the depth caps it.
	substituteDepth int

	// Resolve refs against each document's retrieval URI, with $id inert
	// ([WithRetrievalBase]).
	retrievalBase bool
}

// InlineOption configures [Inline]. Options are produced by this package's
// With* constructors; the interface form (rather than a func type) lets one
// option value serve several entry points, the way [WithRefResolver] serves
// both [ValidateOption] and InlineOption.
type InlineOption interface {
	applyInline(in *inliner)
}

// inlineOptionFunc adapts a function to [InlineOption].
type inlineOptionFunc func(*inliner)

func (f inlineOptionFunc) applyInline(in *inliner) { f(in) }

// WithRetrievalBase makes refs resolve against each document's
// retrieval URI, treating $id as an inert annotation: $id neither
// establishes a base URI nor registers a resolution target, in any
// document. Anchors still resolve within their document, and $id keywords
// pass through to the output verbatim.
//
// Real-world schemas commonly declare a published remote $id while
// shipping the files their refs name alongside the schema; under the
// default RFC behavior those refs absolutize against the remote $id and
// cannot be served from disk. With this option the root document's refs
// absolutize against the base from [WithBaseURI] and each fetched
// document's refs against the URI it was fetched from.
func WithRetrievalBase(enabled bool) InlineOption {
	return inlineOptionFunc(func(in *inliner) { in.retrievalBase = enabled })
}

// RefFailure describes one reference expansion failure to a
// [WithRefFallback] policy.
type RefFailure struct {
	// Err is the expansion failure, wrapping [ErrRefResolve], [ErrRefCycle],
	// or [ErrRefInline].
	Err error

	// Document is the URI of the document containing the referencing
	// schema, distinguishing failures in different documents whose Path
	// values coincide: for the root document its $id or the [WithBaseURI]
	// base ("" when it has neither), and for a fetched document its $id or
	// the URI it was fetched from (under [WithRetrievalBase], always the
	// retrieval URI).
	Document string

	// Path is the JSON Pointer of the referencing schema within its
	// containing document.
	Path string

	// Ref is the reference value that failed to expand.
	Ref string
}

// RefAction is a [RefFallback]'s decision for one failed reference
// expansion. Construct it with [PropagateRef], [DropRef], or
// [SubstituteRef]; the zero value propagates.
type RefAction struct {
	substitute *Schema
	kind       refActionKind
}

// refActionKind discriminates the three [RefAction] behaviors.
type refActionKind int

const (
	refActionPropagate refActionKind = iota
	refActionDrop
	refActionSubstitute
)

// PropagateRef returns the [RefAction] that propagates the original
// expansion error, ending the [Inline] call. It is the zero RefAction.
func PropagateRef() RefAction { return RefAction{} }

// DropRef returns the [RefAction] that drops the failing reference keyword
// while keeping the node's remaining keywords.
func DropRef() RefAction { return RefAction{kind: refActionDrop} }

// SubstituteRef returns the [RefAction] that expands the reference as if it
// had resolved to a copy of s, with the usual draft sibling semantics.
// A nil s drops the reference keyword, as [DropRef] does.
func SubstituteRef(s *Schema) RefAction {
	if s == nil {
		return DropRef()
	}

	return RefAction{kind: refActionSubstitute, substitute: s}
}

// RefFallback decides what happens when [Inline] fails to expand one
// reference, described by the [RefFailure]. ResolveRefFailure returns one of
// the three [RefAction] values: [PropagateRef] propagates the original error,
// ending the Inline call; [DropRef] drops the failing reference keyword and
// keeps the node's remaining keywords; [SubstituteRef] expands the reference
// as if it had resolved to a copy of the given schema. An implementation can
// hold state such as a logger or a table of substitute schemas;
// [RefFallbackFunc] adapts a bare function for policies that need none.
type RefFallback interface {
	// ResolveRefFailure decides the action for one failed reference
	// expansion. The context comes from the [Inline] call in effect, so a
	// policy that fetches a substitute from an external system can honor
	// cancellation and deadlines; a policy that performs no cancellable
	// work can ignore it.
	ResolveRefFailure(ctx context.Context, failure RefFailure) RefAction
}

// RefFallbackFunc adapts a bare decision function to a [RefFallback],
// following [net/http.HandlerFunc].
type RefFallbackFunc func(ctx context.Context, failure RefFailure) RefAction

// ResolveRefFailure calls f.
func (f RefFallbackFunc) ResolveRefFailure(ctx context.Context, failure RefFailure) RefAction {
	return f(ctx, failure)
}

// WithRefFallback sets a per-reference failure policy for [Inline].
// When expanding a reference fails (the target is unresolvable
// ([ErrRefResolve]), the expansion is cyclic ([ErrRefCycle]), or the
// construct has no static expansion ([ErrRefInline], $dynamicRef)), f is
// consulted with a [RefFailure] carrying the URI of the containing document,
// the JSON Pointer path of the referencing schema within that document, the
// reference value, and the error, and its [RefAction] result decides between
// propagating the error ([PropagateRef]), dropping the reference keyword
// ([DropRef]), and expanding a substitute ([SubstituteRef]).
// [RefFallbackFunc] adapts a bare function. A nil f restores the default,
// where every expansion failure is fatal. The consultation runs under the
// Inline call's context, so a policy fetching a substitute can honor
// cancellation and deadlines.
//
// F is consulted once per failure, at the reference that directly failed:
// when a failure surfaces while expanding a nested target, the innermost
// failing ref is consulted with its path in its containing document, and a
// declined consultation propagates the error outward without re-consulting
// at the enclosing refs. A returned schema is deep-copied before splicing
// and is itself inlined recursively, its refs resolving in the context of
// the document containing the failing ref; a cycle introduced by the
// returned schema is an ordinary [ErrRefCycle]. A fallback that keeps
// substituting a schema carrying its own failing ref is bounded: nesting beyond
// an internal depth limit surfaces [ErrRefInline] rather than exhausting the
// stack.
func WithRefFallback(f RefFallback) InlineOption {
	return inlineOptionFunc(func(in *inliner) { in.fallback = f })
}

// Inline returns a deep copy of s in which every $ref is replaced by a copy
// of the schema it targets, producing a self-contained schema. S and
// resolver-returned schemas are never mutated. A nil s returns nil.
//
// Fragment-only refs (#/pointer, #anchor) resolve within the enclosing
// document using the same $id/$anchor registry the validator builds. Other
// refs are absolutized against the enclosing resource's base URI (its $id,
// or the base from [WithBaseURI], with a schemeless base normalized
// against file:///) and fetched through the resolver given via
// [WithRefResolver]; any fragment is then evaluated against the fetched
// document. Fetched documents are inlined recursively using their own base
// URIs, and each document is fetched at most once per Inline call. Every
// ref resolves against its document's original structure, exactly as the
// validator would, so expanding one ref never changes what a later ref's
// JSON Pointer or anchor addresses. [WithRetrievalBase] switches ref
// resolution to each document's retrieval URI, treating $id as an inert
// annotation.
//
// Sibling keywords beside $ref are handled per draft semantics, with the
// draft detected from the root schema's $schema exactly as the validator
// detects it (fetched documents follow the root document's draft, matching
// how validation applies one draft throughout). Under Draft 2020-12 the
// node keeps its sibling keywords and the target copy joins the node's
// allOf, preserving both the conjunction and the annotation flow that the
// unevaluated* keywords depend on. Under Draft 7 siblings of $ref are
// ignored, so the node is replaced by the target copy alone. A node whose
// only keyword is $ref is replaced by the target copy alone under either
// draft. A spliced copy never carries a $schema keyword, and the returned
// root keeps the input's $schema.
//
// Refs are inlined only in the typed sub-schema positions [SubschemaEntries]
// covers; a $ref carried as raw JSON inside an unknown keyword is left
// as-is, although a ref pointing into such a position still resolves.
//
// A ref whose expansion reaches its own target returns an error wrapping
// [ErrRefCycle]. A $dynamicRef under Draft 2020-12 has no faithful static
// expansion and returns an error wrapping [ErrRefInline] (Draft 7 ignores
// the keyword, as the validator does). A non-local ref with no resolver, or
// an unresolvable target, returns an error wrapping [ErrRefResolve].
// [WithRefFallback] sets a per-reference policy that can turn any of
// these failures into dropping the reference keyword or expanding a
// substitute schema instead.
//
// The context is passed to the [RefResolver] (see [WithRefResolver]) with
// every document fetch, so a resolver that fetches over the network can
// honor cancellation and deadlines.
//
// Inline is one-shot sugar for [NewInliner] plus [Inliner.Inline], applying
// its options per call; to inline many documents under one option set,
// build the [Inliner] once and reuse it.
func Inline(ctx context.Context, s *Schema, opts ...InlineOption) (*Schema, error) {
	return NewInliner(opts...).Inline(ctx, s)
}

// Inliner inlines schemas under one fixed option set, completing the
// reusable trio with [Generator] and [Validator]: [NewInliner] applies the
// options once and the returned Inliner is reused, so a caller inlining
// many documents against one resolver configuration neither re-passes nor
// re-applies the option slice per call.
//
// An Inliner is safe for concurrent use by multiple goroutines, provided
// the configured hooks are: the configuration is only read during inlining,
// and every run keeps its own state, including its own document fetches,
// since fetched documents are resolved relative to each input.
type Inliner struct {
	proto *inliner
}

// NewInliner returns an [Inliner] with the given options applied. Nil
// options are skipped, so an optional option can be passed unconditionally.
func NewInliner(opts ...InlineOption) *Inliner {
	proto := &inliner{}

	for _, opt := range opts {
		if opt != nil {
			opt.applyInline(proto)
		}
	}

	proto.baseURI = uriref.NormalizeBaseURI(proto.baseURI)

	return &Inliner{proto: proto}
}

// Inline returns a deep copy of s with every $ref expanded under the
// Inliner's options. The semantics, including the nil result for a nil s,
// follow the package-level [Inline], whose documentation is authoritative.
func (il *Inliner) Inline(ctx context.Context, s *Schema) (*Schema, error) {
	if s == nil {
		return nil, nil //nolint:nilnil // A nil schema inlines to nil.
	}

	// The run copies the prototype's configuration and carries fresh
	// per-call state, so concurrent runs from one Inliner never share
	// mutable state.
	in := &inliner{
		ctx:           ctx,
		resolver:      il.proto.resolver,
		fallback:      il.proto.fallback,
		draftOverride: il.proto.draftOverride,
		baseURI:       il.proto.baseURI,
		retrievalBase: il.proto.retrievalBase,
		inflight:      map[*Schema]bool{},
		memo:          map[*Schema]*Schema{},
		paths:         map[*Schema]string{},
		docs:          map[*Schema]string{},
	}

	// The context reaches the resolver through the ctx field set above:
	// document fetches happen deep inside the expansion walk, which cannot
	// thread a parameter through the shared resolution machinery.
	//nolint:contextcheck // See the comment above.
	return in.run(s)
}

// run inlines s under the receiver's configuration and per-call state.
func (in *inliner) run(s *Schema) (*Schema, error) {
	// Two clones of the document: the pristine copy carries the registries
	// and answers every ref-target resolution, while the working copy
	// receives the expansions and becomes the result. Both are clones of
	// the same input, so they are structurally identical and walk in
	// lockstep.
	pristine, err := cloneSchema(s)
	if err != nil {
		return nil, err
	}

	working, err := cloneSchema(s)
	if err != nil {
		return nil, err
	}

	// The same registry construction Compile performs, seeded with the
	// configured base URI: the walk registers every $id, $anchor, and
	// $dynamicAnchor and records each schema's base URI, which is what
	// fragment-only resolution and ref absolutization consult. Only
	// pristine copies are registered, so no resolution can observe a
	// mutation. In retrieval-base mode the walk treats $id as inert, so
	// every schema's base URI stays the document's retrieval URI and $id
	// registers nothing.
	draft := detectDraft(pristine)
	if in.draftOverride != nil {
		draft = *in.draftOverride
	}

	in.v = &validator{root: pristine, draft: draft, inertIDs: in.retrievalBase}
	in.v.initRegistries()
	in.v.walkSchema(pristine, in.baseURI)
	in.recordPaths(pristine, "", in.v.schemaBase(pristine))

	// Register the root document under its base URI when its own $id did
	// not already claim one, so a ref that absolutizes back to the root
	// document resolves to this copy instead of re-fetching it.
	if in.baseURI != "" {
		if _, ok := in.v.uriRegistry[in.baseURI]; !ok {
			in.v.uriRegistry[in.baseURI] = pristine
		}
	}

	// The context reaches the resolver through the ctx field set above:
	// document fetches happen deep inside the expansion walk, which cannot
	// thread a parameter through the shared resolution machinery.
	//nolint:contextcheck // See the comment above.
	err = in.walkPair(working, pristine, "")
	if err != nil {
		return nil, err
	}

	// A root that was itself a ref node may have been replaced wholesale by
	// a target copy, which never carries $schema; the returned document
	// keeps the input's dialect.
	working.Schema = s.Schema

	return working, nil
}

// recordPaths maps every schema in the pristine document rooted at s to its
// JSON Pointer path within that document and to doc, the document's URI,
// keyed by pointer identity. The paths and document URIs name ref-node
// locations for fallback consultations. An aliased or cyclic graph keeps the
// first location recorded for a node.
func (in *inliner) recordPaths(s *Schema, path, doc string) {
	if s == nil {
		return
	}

	if _, ok := in.paths[s]; ok {
		return
	}

	in.paths[s] = path
	in.docs[s] = doc

	for _, child := range SubschemaEntries(s) {
		in.recordPaths(child.Schema, path+child.Pointer, doc)
	}
}

// walkPair makes working's subtree self-contained in place, reading all
// structure from its pristine counterpart. The two trees are clones of the
// same document and [SubschemaEntries] returns children in deterministic order, so
// the walk pairs nodes position by position; path is the pristine node's
// JSON Pointer location within its containing document, extended token by
// token as the walk descends. A $ref is resolved against pristine structure,
// its target's self-contained copy is built by inlineCopy, and the copy is
// spliced into working per the draft's sibling rules. Spliced copies have no
// pristine counterpart and are already self-contained, so the walk never
// descends into them.
func (in *inliner) walkPair(working, pristine *Schema, path string) error {
	// Self-contained copies to join the node's allOf after its children are
	// walked: a Draft 2020-12 $ref target, a fallback substitute for a
	// $dynamicRef, or both.
	var copies []*Schema

	if in.v.draft == Draft2020 && pristine.DynamicRef != "" {
		inlineErr := fmt.Errorf("%w: $dynamicRef %q has no static expansion", ErrRefInline, pristine.DynamicRef)

		tc, err := in.substitute(pristine, path, pristine.DynamicRef, inlineErr)
		if err != nil {
			return err
		}

		// The fallback handled the keyword: it is dropped from the node, and
		// any substitute splices exactly as a resolved target would.
		working.DynamicRef = ""

		if tc != nil {
			rest := *pristine
			rest.DynamicRef = ""

			if IsTrueSchema(&rest) {
				*working = *tc

				return nil
			}

			copies = append(copies, tc)
		}
	}

	if pristine.Ref != "" {
		tc, replace, err := in.expand(pristine, path)
		if err != nil {
			return err
		}

		working.Ref = ""

		if replace && len(copies) == 0 {
			// Draft-07 ignores siblings of $ref, so the node is replaced by
			// the target copy alone; a bare ref (no siblings) is replaced
			// directly under either draft. The copy is self-contained and
			// the node's pristine children no longer correspond to anything
			// in working, so the walk stops here. A $dynamicRef substitute
			// already queued in copies must not be discarded, so the wholesale
			// replace is taken only when nothing else needs joining.
			*working = *tc

			return nil
		}

		// A nil tc with no error means the fallback dropped the reference
		// keyword; the node's remaining keywords and children stay.
		if tc != nil {
			copies = append(copies, tc)
		}
	}

	workingChildren := SubschemaEntries(working)
	pristineChildren := SubschemaEntries(pristine)

	// The working and pristine nodes are structurally identical here: pristine
	// is a deep copy taken before any keyword was cleared, and clearing a scalar
	// ref keyword does not change the subschema child set. The guard is defensive
	// against a future divergence, so positional pairing cannot panic on a
	// length mismatch or silently misalign children.
	if len(workingChildren) != len(pristineChildren) {
		return fmt.Errorf("%w: subschema child count diverged at %q (%d vs %d)",
			ErrRefInline, path, len(workingChildren), len(pristineChildren))
	}

	for i, p := range pristineChildren {
		err := in.walkPair(workingChildren[i].Schema, p.Schema, path+p.Pointer)
		if err != nil {
			return err
		}
	}

	// Draft 2020-12 evaluates $ref alongside its siblings as a conjunction.
	// Keeping the siblings in place and joining the target copy to the
	// node's allOf preserves that: every assertion still applies, and
	// annotations from the target still surface at the node for the
	// unevaluated* keywords, which moving the siblings into a separate
	// allOf branch would break. The copies join after the children are
	// walked so the child lists stay paired during the walk.
	working.AllOf = append(working.AllOf, copies...)

	return nil
}

// expand resolves the $ref at the pristine node and returns a self-contained
// copy of its target, plus whether the draft's sibling rules call for
// replacing the ref node wholesale (Draft 7, or a node whose only keyword is
// $ref) rather than joining the copy to the node's allOf. A nil copy with a
// nil error means the fallback dropped the reference keyword.
func (in *inliner) expand(pristine *Schema, path string) (*Schema, bool, error) {
	tc, err := in.expandTarget(pristine, path)
	if err != nil || tc == nil {
		return nil, false, err
	}

	rest := *pristine
	rest.Ref = ""

	// A Draft 2020-12 $dynamicRef is resolved before the $ref and already
	// cleared from working, so it no longer counts as a sibling that would keep
	// the node from being a bare ref eligible for wholesale replacement.
	rest.DynamicRef = ""

	replace := in.v.draft == Draft7 || IsTrueSchema(&rest)

	return tc, replace, nil
}

// expandTarget produces the self-contained copy the $ref at the pristine
// node expands to. A failure directly at this node (an unresolvable target
// or a cycle closed by this ref) consults the fallback here, with the
// node's path in its containing document; an error from a nested expansion
// already consulted at the inner failing ref and propagates unchanged. A nil
// copy with a nil error means the fallback dropped the reference keyword.
func (in *inliner) expandTarget(pristine *Schema, path string) (*Schema, error) {
	ref := pristine.Ref

	target, targetDoc, targetPtr, err := in.resolveTarget(pristine, ref)
	if err != nil {
		return in.substitute(pristine, path, ref, err)
	}

	if in.inflight[target] {
		return in.substitute(pristine, path, ref, fmt.Errorf("%w: %q", ErrRefCycle, ref))
	}

	// A target materialized from an unknown (Extra) keyword via a JSON pointer
	// is a fresh schema recordPaths never walked, so it has no recorded path or
	// document. Seed it (idempotently) with its own document and pointer so a
	// nested ref failure reports the document it physically lives in. A
	// fragment-only ref (empty targetDoc) shares the referencing node's
	// document, so fall back to that node's location.
	if _, ok := in.paths[target]; !ok {
		if targetDoc == "" {
			targetDoc, targetPtr = in.docs[pristine], path
		}

		in.recordPaths(target, targetPtr, targetDoc)
	}

	return in.inlineCopy(target, in.paths[target], true)
}

// maxSubstituteDepth bounds nested [SubstituteRef] expansions so a fallback
// that always substitutes a schema carrying its own failing ref surfaces an
// [ErrRefInline] rather than recursing until the stack is exhausted.
const maxSubstituteDepth = 100

// substitute consults the [WithRefFallback] policy for a reference
// that failed directly at the pristine node and turns its answer into a
// spliceable self-contained copy. With no fallback configured, or on
// [PropagateRef], the original inlineErr is returned. [DropRef] yields
// (nil, nil): the caller drops the reference keyword. A [SubstituteRef]
// schema is deep-copied, registered in resolution space as if written at
// the failing node's location (its base URI is the node's, so its refs
// resolve in the context of the document containing the failing ref), and
// inlined recursively into a self-contained copy.
func (in *inliner) substitute(pristine *Schema, path, ref string, inlineErr error) (*Schema, error) {
	if in.fallback == nil {
		return nil, inlineErr
	}

	// A substitute can itself contain a failing ref whose fallback substitutes
	// again, and each clone is a fresh schema the inflight cycle guard cannot
	// match, so bound the nesting to keep a pathological fallback from
	// exhausting the stack.
	if in.substituteDepth >= maxSubstituteDepth {
		return nil, fmt.Errorf("%w: substitution exceeded %d nested levels at %q",
			ErrRefInline, maxSubstituteDepth, ref)
	}

	in.substituteDepth++
	defer func() { in.substituteDepth-- }()

	action := in.fallback.ResolveRefFailure(in.runContext(),
		RefFailure{Document: in.docs[pristine], Path: path, Ref: ref, Err: inlineErr})

	if action.kind == refActionPropagate {
		return nil, inlineErr
	}

	if action.kind == refActionDrop {
		return nil, nil //nolint:nilnil // The caller drops the reference keyword.
	}

	cp, err := cloneSchema(action.substitute)
	if err != nil {
		return nil, err
	}

	// Register the substitute's $id/$anchor in the per-run fallback registries
	// via a scratch validator rather than the shared ones. A caller-supplied
	// substitute whose $id collides with an already-loaded document URI must not
	// overwrite that entry; the fallback is consulted only after the shared
	// registry, so the real document keeps priority while the substitute's own
	// nested refs still resolve.
	in.v.registerFallbackSchema(cp, in.v.schemaBase(pristine))
	in.recordPaths(cp, path, in.docs[pristine])

	return in.inlineCopy(cp, path, false)
}

// inlineCopy returns a self-contained copy of the pristine target: a fresh
// clone whose refs are expanded by the same pristine-space resolution as the
// rest of the run, leaving the target itself untouched; path is the target's
// JSON Pointer location within its containing document, seeding the walk's
// path tracking. When memoize is set, the completed target is recorded so one
// referenced from several places is expanded once; every additional use clones
// the memoized copy so no two positions in the output share nodes. A
// substitute-originated copy passes memoize false: its pointer is fresh and
// never resolved again, so memoizing it would only accumulate dead entries. The
// inflight set marks targets whose copy is still being built: a ref resolving
// to one means the expansion reached its own target, which only a reference
// cycle can cause.
func (in *inliner) inlineCopy(target *Schema, path string, memoize bool) (*Schema, error) {
	if memoized, ok := in.memo[target]; ok {
		return cloneSchema(memoized)
	}

	in.inflight[target] = true
	defer delete(in.inflight, target)

	cp, err := cloneSchema(target)
	if err != nil {
		return nil, err
	}

	// The $schema dialect declaration belongs to a document, not to a
	// spliced sub-schema; the output keeps the root document's dialect.
	cp.Schema = ""

	err = in.walkPair(cp, target, path)
	if err != nil {
		return nil, err
	}

	if memoize {
		in.memo[target] = cp

		// Clone on the first use too, so the memo entry is never aliased to a
		// position in the output tree. Every caller, first or later, then gets an
		// independent copy, and no downstream mutation of one placement can leak
		// into another through a shared memo node.
		return cloneSchema(cp)
	}

	// A non-memoized copy (a substitute or $dynamicRef expansion) is freshly
	// built here and never stored in the memo or aliased anywhere, so it is
	// returned directly: a second deep clone would only duplicate work.
	return cp, nil
}

// resolveTarget resolves the ref at the pristine node to its pristine target
// schema. Fragment-only refs resolve within the enclosing document through
// the shared registries; other refs absolutize against the node's base URI,
// fetch the addressed document (served from the registry when already
// loaded), and evaluate any fragment against it. Every unresolvable form
// returns an error wrapping [ErrRefResolve].
// It also returns the target's own containing-document URI and its JSON Pointer
// within that document, so a caller seeding paths for an otherwise-unrecorded
// target (one materialized from an unknown keyword) reports a nested failure in
// the document it physically lives in. A fragment-only ref returns an empty
// document, signaling the caller to use the referencing node's own document.
func (in *inliner) resolveTarget(node *Schema, ref string) (*Schema, string, string, error) {
	if uriref.IsFragmentOnly(ref) {
		target := in.v.resolveRef(node, ref)
		if target == nil {
			return nil, "", "", fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, ref)
		}

		return target, "", "", nil
	}

	base := in.v.schemaBase(node)
	absRef := uriref.ResolveURI(base, ref)

	parsed, err := url.Parse(absRef)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: parse %q: %w", ErrRefResolve, absRef, err)
	}

	fragment := parsed.Fragment
	rawFrag, encoded := uriref.RawFragment(parsed)
	parsed.Fragment = ""
	parsed.RawFragment = ""
	baseURI := parsed.String()

	docRoot, ok := in.v.lookupURI(baseURI)
	if !ok {
		docRoot, err = in.fetchDoc(baseURI)
		if err != nil {
			return nil, "", "", err
		}
	}

	if fragment == "" {
		return docRoot, baseURI, "", nil
	}

	// JSON Pointer within the fetched document. Pass the still-encoded
	// fragment so a member name escaped as %2F is not mistaken for a
	// pointer separator.
	if strings.HasPrefix(fragment, "/") {
		target := in.v.resolveJSONPointer(docRoot, rawFrag, encoded)
		if target == nil {
			return nil, "", "", fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, ref)
		}

		return target, baseURI, fragment, nil
	}

	// Anchor within the fetched document, resolved via the shared cross-document
	// precedence (retrieval base first, then the document's canonical $id base)
	// so Inline resolves an anchor exactly as validation would.
	if target, ok := in.v.lookupAnchorWithFallback(baseURI, docRoot, fragment); ok {
		return target, baseURI, "", nil
	}

	return nil, "", "", fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, ref)
}

// runContext returns the [Inline] call's context for hook invocations (the
// [RefResolver], the [RefFallback] policy), falling back to
// [context.Background] when none was set.
func (in *inliner) runContext() context.Context {
	if in.ctx == nil {
		return context.Background()
	}

	return in.ctx
}

// callResolver invokes the configured resolver for uri under the
// [Inline] call's context. It mirrors [validator.callResolver], including
// normalizing a nil schema with a nil error to the not-resolved answer
// (ErrNotResolved), upholding the [RefResolver] contract that no caller
// dereferences a nil document.
func (in *inliner) callResolver(uri string) (*Schema, error) {
	s, err := in.resolver.ResolveRef(in.runContext(), uri)
	if err != nil {
		//nolint:wrapcheck // fetchDoc wraps the error with ErrRefResolve.
		return nil, err
	}

	if s == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotResolved, uri)
	}

	return s, nil
}

// fetchDoc fetches the document at baseURI through the configured resolver,
// registers a pristine copy under baseURI, and returns the copy. The copy is
// resolution space only and is never mutated; output material is cloned from it
// on demand.
//
// Its own $ids, anchors, and base URIs are registered through the per-run
// fallback registries rather than walked into the shared ones: a fetched
// document whose nested $id resolves to an already-loaded URI (the root base or
// an earlier document) must not overwrite that entry, so the already-loaded
// document keeps priority while the fetched document's own refs still resolve.
// This mirrors the substitute path's convention.
func (in *inliner) fetchDoc(baseURI string) (*Schema, error) {
	if in.resolver == nil {
		return nil, fmt.Errorf("%w: no resolver configured for %q", ErrRefResolve, baseURI)
	}

	s, err := in.callResolver(baseURI)
	if errors.Is(err, ErrNotResolved) {
		return nil, fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, baseURI)
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	cp, err := cloneSchema(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	in.v.uriRegistry[baseURI] = cp
	in.v.registerFallbackSchema(cp, baseURI)
	in.recordPaths(cp, "", in.v.schemaBase(cp))

	return cp, nil
}

// FileResolver is a [RefResolver] that serves file-path and relative URIs
// from an [io/fs.FS], unmarshaling each referenced file as a JSON schema
// document; a referenced file that does not contain one is an error.
// Construct it with [NewFileResolver]; pair [os.DirFS] with
// [WithBaseURI] to inline schemas that reference each other by
// relative file path.
//
// A "file://" scheme, any authority, and leading slashes are dropped, so URIs
// are resolved relative to the fs root: relative refs absolutize against the
// normalized base URI into file URIs (base "main.json" plus ref
// "sub/child.json" yields "file:///sub/child.json"), which reduce back to
// paths addressing the fs from its root (file://host/sub.json and
// file:////sub.json both map to "sub.json"). The remaining path is used verbatim
// as the [io/fs] file name, so [io/fs] confines resolution to the fs root:
// a ref escaping above it is not a valid fs path, and [Inline] surfaces the
// read failure as an error wrapping [ErrRefResolve].
//
// The resolver works the same way with [WithRefResolver] during validation:
// refs that reach the resolver as relative or file URIs are served from
// the fs. Refs that absolutize to another scheme (an http $id, for example)
// are not valid fs paths and resolve to an error; [StripPrefix] wraps the
// resolver to strip the published remote base from each URI first so those
// refs can be served from the fs.
type FileResolver struct {
	fsys fs.FS
}

// NewFileResolver returns a [FileResolver] serving schema documents from
// fsys.
func NewFileResolver(fsys fs.FS) *FileResolver {
	return &FileResolver{fsys: fsys}
}

// ResolveRef reads and parses the schema document stored at the file path
// named by uri. The resolver is authoritative for its fs, so an unreadable or
// undecodable file is an error rather than the not-resolved answer. Parsing
// goes through [ParseSchema], so a file whose top-level JSON is not an object
// or boolean (a number, string, array, or null) is rejected rather than
// silently producing a degenerate schema. Reads are local and not cancellable,
// so the context is unused. See [FileResolver] for the path semantics.
func (r *FileResolver) ResolveRef(_ context.Context, uri string) (*Schema, error) {
	name := uriref.FilePathFromURI(uri)

	data, err := fs.ReadFile(r.fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read schema document: %w", err)
	}

	s, err := ParseSchema(data)
	if err != nil {
		return nil, fmt.Errorf("decode schema document %q: %w", name, err)
	}

	return s, nil
}
