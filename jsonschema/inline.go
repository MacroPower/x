package jsonschema

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/refresolve"
	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// inliner carries the configuration and per-call state of one [Inline] run:
// the functional options, a [refresolve.Session] holding the $id/$anchor
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

	// The resolution session resolving references, sharing the core the
	// validator uses. Its URI, anchor, and base-URI registries are built by the
	// same walk Compile uses, over the pristine root copy and each pristine
	// fetched-document copy, so resolution matches the validator's and sees only
	// original structure.
	session *refresolve.Session

	// The per-reference failure policy from [WithRefFallback]; nil
	// means every expansion failure is fatal.
	fallback RefFallback

	// The WithDraft override; nil leaves the draft to $schema detection.
	draftOverride *Draft

	baseURI string

	// The node-identity index over every pristine schema the run touches:
	// the pristine root document, each fetched document, each fallback
	// substitute, and each target materialized from an unknown keyword. As record
	// walks, it interns each schema here, and the per-node slices below are
	// indexed by the id it assigns, so one identity source backs all the
	// expansion bookkeeping. Pristine schemas are never mutated, so their
	// pointers stay stable keys for the run.
	index *schemaIndex

	// The run's structural vetter, minting the [schemavet.Doc] currency for
	// the pristine root and each fallback substitute, and the [schemavet.Node]
	// currency for each JSON-pointer fallback target the session materializes.
	// Those passes share the vetter's visited sets, so the vetter checks a
	// node reached twice only once. Each fetched document gets a fresh vetter
	// in [inliner.fetchDoc] instead, since a remote is independent of the
	// document that referenced it.
	vetter *schemavet.Vetter

	// The inflight[id] flag marks a pristine schema whose self-contained copy is
	// currently being built; a ref that resolves to an in-flight schema is a cycle.
	inflight []bool

	// The memo[id] entry is a pristine schema's finished self-contained copy, so
	// a target referenced from several places is expanded once. Every additional
	// use clones the memoized copy, so no two splice positions share nodes.
	// Sharing within one copied document survives, since the copy reproduces
	// the graph it was taken from. A resolver-returned document or a
	// WithRefFallback substitute can carry such sharing, since neither is
	// tree-checked.
	memo []*Schema

	// The working nodes walkPair has already expanded. A working copy mirrors
	// the aliasing of the document it was cloned from, and expansion writes the
	// node in place, so the walk expands such a node once and both positions
	// read that one expansion. Working nodes are not interned (only pristine
	// ones are), so the set keys on the pointer rather than on a node id.
	expanded map[*Schema]bool

	// The refusal [NewInliner] records when the [WithBaseURI] value does not
	// parse. Every [Inliner.Inline] call returns it, since the constructor has
	// no error to return.
	baseURIErr error

	// The paths[id] entry is a pristine schema's JSON Pointer path within its
	// containing document, recorded when each document joins resolution space. The
	// paths name ref-node locations for [WithRefFallback] consultations and seed
	// the path of each expansion walk.
	paths []string

	// The docs[id] entry is the URI of a pristine schema's containing document,
	// recorded alongside paths: the root document's $id or [WithBaseURI] base
	// ("" when it has neither), and each fetched document's $id or retrieval URI.
	// The URIs identify the failing document in [WithRefFallback] consultations.
	docs []string

	// The draft governing the Draft 2020-12/Draft 7 sibling rules, detected from
	// the root document (or the [WithDraft] override). It is the parent Draft,
	// distinct from the resolution core's own enum.
	draft Draft

	// Count of fallback consultations caused by a ref closing on an in-flight
	// target. Unlike a resolution failure, which fails identically wherever the
	// ref is expanded from, a cycle truncation depends on the inflight stack of
	// the expansion that hit it, so a copy built while the counter moved is
	// context-dependent and must not be memoized (see [inliner.inlineCopy]).
	cycleFallbacks int

	// Current depth of nested substitute expansions. Each [SubstituteRef]
	// clone is a fresh schema the pointer-identity inflight guard never
	// matches, so a fallback that substitutes a schema with its own failing
	// ref would recurse without bound; the depth caps it.
	substituteDepth int

	// The per-draft behavioral policy, resolved once from draft. All-bool, so it
	// groups with retrievalBase in the struct's trailing small fields.
	profile draftProfile

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
// pass through to the output verbatim. An inert $id addresses nothing, so the
// structural vet skips its domain check, in the root, in each substitute, and
// in every fetched document.
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
// returned schema is an ordinary [ErrRefCycle]. The copy enters resolution
// space as a document of its own, so Inline vets it as one. A violation ends
// the Inline call with the check's sentinel, in a message naming the
// reference the substitute answered. A fallback that keeps
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
// The root document's sub-schema pointers must form a tree, the same demand
// [Compile] makes. A root reaching one *Schema through two paths, or through
// a pointer cycle, returns an error wrapping [ErrSchemaNotTree], as does one
// whose loop closes through a value field (Const, Enum, Examples, or Extra).
// Compile refuses all three shapes, wording each refusal exactly as Inline
// does.
//
// Inline then vets the root structurally, the pass Compile runs over the
// document it is given, before any reference resolves. A violation returns the
// check's sentinel ([ErrInvalidType], [ErrItemsArrayUnderDraft2020],
// [ErrNegativeBound], [ErrNonPositiveMultipleOf], [ErrNilSubschema],
// [ErrConflictingSchemaFields], [ErrDuplicatePropertyOrder], [ErrInvalidID],
// or [ErrMisplacedVocabulary]) naming the offending path, so the two engines
// refuse the same roots for the same structural causes. Both engines refuse an
// unparsable [WithBaseURI] value with [ErrInvalidBaseURI].
// [WithRetrievalBase] carves out the $id domain check, in the root, in each
// substitute, and in every fetched document, since an inert $id addresses
// nothing. One compile-time refusal has no Inline counterpart,
// [ErrUnknownVocabulary], which belongs to the vocabulary resolution Inline
// does not run. The agreement therefore covers the structural vet rather than
// every error.
//
// Inline then runs the same reference-closure walk as Compile before expanding
// anything. It fetches and vets every document a reference reaches, however
// many documents deep, so the walk holds a document reachable only through
// another document's reference to the same policy as one the root names
// outright. [WithRefFallback] suspends the walk's refusals apart from an
// identifier collision, since a fallback answers one failing reference at a
// time and a document outside the expansion has no reference in the expansion
// for a policy to answer.
//
// A ref whose expansion reaches its own target returns an error wrapping
// [ErrRefCycle]. A $dynamicRef under Draft 2020-12 has no faithful static
// expansion and returns an error wrapping [ErrRefInline] (Draft 7 ignores the
// keyword, as the validator does). A non-local ref with no resolver, or an
// unresolvable target, returns an error wrapping [ErrRefResolve].
// [WithRefFallback] sets a per-reference policy that can turn any of these
// failures into dropping the reference keyword or expanding a substitute
// schema instead.
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

	base, err := uriref.ParseBaseURI(proto.baseURI)
	if err != nil {
		proto.baseURIErr = fmt.Errorf("%w: %w", ErrInvalidBaseURI, err)
	}

	proto.baseURI = base

	return &Inliner{proto: proto}
}

// Inline returns a deep copy of s with every $ref expanded under the
// Inliner's options. The semantics, including the nil result for a nil s,
// follow the package-level [Inline], whose documentation is authoritative.
func (il *Inliner) Inline(ctx context.Context, s *Schema) (*Schema, error) {
	if s == nil {
		return nil, nil //nolint:nilnil // A nil schema inlines to nil.
	}

	// Inline refuses a base URI that is not a URI reference before anything
	// absolutizes against it, the check Compile makes. NewInliner has no error
	// to return, so it records the refusal and every Inline call reports it.
	// The nil-schema answer above still comes first, the order Compile uses
	// when it refuses a nil schema before reading the base.
	if il.proto.baseURIErr != nil {
		return nil, il.proto.baseURIErr
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
		index:         newSchemaIndex(),
		expanded:      map[*Schema]bool{},
	}

	// The context reaches the resolver through the ctx field set above:
	// document fetches happen deep inside the expansion walk, which cannot
	// thread a parameter through the shared resolution machinery.
	//nolint:contextcheck // See the comment above.
	return in.run(s)
}

// run inlines s under the receiver's configuration and per-call state.
func (in *inliner) run(s *Schema) (*Schema, error) {
	// The tree check runs before the clones, so a root the inliner cannot
	// expand fails before the run copies anything. It reads the sub-schema
	// graph, and the pristine clone below reports a loop closing through a
	// value field. Compile makes both demands too.
	err := checkSchemaTree(s)
	if err != nil {
		return nil, err
	}

	// Two clones of the document: the pristine copy carries the registries
	// and answers every ref-target resolution, while the working copy
	// receives the expansions and becomes the result. Both are clones of
	// the same input, so they are structurally identical and walk in
	// lockstep.
	pristine, err := cloneCheckedSchema(s, "the root document")
	if err != nil {
		return nil, err
	}

	working := cloneSchema(s)

	// The same registry construction Compile performs, seeded with the
	// configured base URI: the walk registers every $id, $anchor, and
	// $dynamicAnchor and records each schema's base URI, which is what
	// fragment-only resolution and ref absolutization consult, and registers the
	// root document under its base URI when its own $id did not claim one. Only
	// pristine copies are registered, so no resolution can observe a mutation.
	// In retrieval-base mode the walk treats $id as inert, so every schema's
	// base URI stays the document's retrieval URI and $id registers nothing.
	draft, err := resolveDraft(pristine, in.draftOverride)
	if err != nil {
		return nil, err
	}

	in.draft = draft
	in.profile = in.draft.profile()

	reg := refresolve.NewRegistry(refDeps(), toRefDraft(in.draft), in.retrievalBase)
	reg.Build(pristine, in.baseURI)

	in.vetter = schemavet.NewVetter(in.vetProfile())

	// Vet the root structurally, the pass Compile runs over the document it is
	// given. It covers field structure, identifiers, type names, bound
	// domains, and under 2020-12 the items array form, so the two engines
	// refuse the same roots for the same structural causes, and the minted
	// currency is what enters resolution space below.
	rootDoc, err := in.vetter.VetDoc(pristine, "", in.baseURI)
	if err != nil {
		//nolint:wrapcheck // The vetting error already names the document and path.
		return nil, err
	}

	// A JSON-pointer fallback target (a sub-schema carried as raw JSON in an
	// unknown keyword) is materialized fresh by the session and spliced into
	// the output, so [inliner.fallbackVet] checks it at materialization
	// through the run's vetter; without this an ill-formed target would inline
	// into a malformed output schema this package's own [Compile] rejects.
	in.session = reg.NewSession(in.fallbackVet)

	in.recordDoc(rootDoc, "", in.session.SchemaBase(pristine))

	// Fetch and vet the whole reference closure before expanding any of it.
	// The walk holds a document reachable only through another document's
	// reference to the policy Compile holds it to.
	//
	// The context reaches the resolver through the ctx field set above:
	// document fetches happen deep inside the walks, which cannot thread a
	// parameter through the shared resolution machinery.
	//nolint:contextcheck // See the comment above.
	err = in.walkClosure(pristine)
	if err != nil {
		return nil, err
	}

	//nolint:contextcheck // See the context comment above walkClosure.
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

// walkClosure fetches and vets every document the run's references reach,
// through the same [refClosure] walk Compile runs, so both engines hold the
// same documents and refuse the same graphs. The run vets the root and its own
// nested resources before it builds the session, so the walk finds them
// already checked.
//
// [WithRefFallback] chooses the mode. With no fallback the walk is strict and
// fetches through [inliner.prefetchDoc], so [inliner.closureDoc] vets each
// document as the frontier reaches it, and a violation anywhere in the closure
// refuses the run.
//
// With a fallback the walk is tolerant and fetches through [inliner.fetchDoc],
// which checks for an identifier collision, then vets, then registers, and
// records every failure in the negative cache. The walk then refuses nothing
// but that collision, which no substitute can answer: a fallback answers one
// failing reference at a time and cannot decide which of two documents owns a
// URI they both claim. A document no reference in the expansion reaches has no
// reference in the expansion for a policy to answer, so walkPair replays each
// other failure at the references that do reach it.
//
// The mode decides a target that [inliner.fallbackVet] rejects the same way it
// decides a document. Under a strict walk the rejection refuses the run at
// materialization, the same point at which Compile refuses it. Under a
// tolerant walk it waits in the session's pointer cache until the ref that
// expands the target consults the policy through [inliner.expandTarget].
//
// The walk resolves a $dynamicRef to reach the document it names, but a
// $dynamicRef that resolves to nothing never refuses the walk, whatever the
// mode. Inline has no static expansion for the keyword and answers
// [ErrRefInline] wherever walkPair meets one, and a strict pre-walk would
// displace that answer with a resolution error.
func (in *inliner) walkClosure(pristine *Schema) error {
	strict := in.fallback == nil

	fetch := in.fetchDoc
	if strict {
		fetch = in.prefetchDoc
	}

	err := refClosure{
		session:    in.session,
		fetch:      fetch,
		root:       pristine,
		onDoc:      in.closureDoc,
		dynamicRef: in.profile.dynamicRef,
		strictRef:  strict,
		strictDyn:  false,
	}.run()
	if err != nil {
		// A vetting failure already carries the sentinel; only a resolution
		// failure needs Inline's own, which the contract names for every
		// reference that does not resolve.
		if errors.Is(err, ErrRefResolve) {
			return err
		}

		return fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	return nil
}

// closureDoc is the closure walk's per-document hook. It vets a document the
// walk reached and records it in the run's index. A document already interned
// needs neither. It arrived as the pristine root, or through
// [inliner.fetchDoc], which vets and records what it registers.
//
// Each document gets a fresh vetter, since a remote is independent of the
// document that referenced it.
func (in *inliner) closureDoc(s *Schema, uri string) error {
	if _, ok := in.index.nodeID(s); ok {
		return nil
	}

	doc, err := schemavet.NewVetter(in.vetProfile()).VetDoc(s, uri+"#", uri)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	in.recordDoc(doc, "", in.session.SchemaBase(s))

	return nil
}

// recordDoc records a vetted document: the pristine root, a fetched remote, or
// a fallback substitute, each of which enters resolution space as a document of
// its own. The [schemavet.Doc] parameter states in the signature that only
// vetted material reaches the index and the expansion bookkeeping keyed by
// it, the demand [schemaIndex.extend] makes of the validator's own index.
func (in *inliner) recordDoc(doc schemavet.Doc, path, docURI string) {
	in.recordTree(doc.Schema(), path, docURI)
}

// recordNode records a vetted fragment: a JSON-pointer fallback target the
// session materialized from raw JSON inside an unknown keyword, which is a
// fragment of a document rather than one of its own and so carries the
// [schemavet.Node] currency (see [inliner.vetTarget]).
func (in *inliner) recordNode(node schemavet.Node, path, docURI string) {
	in.recordTree(node.Schema(), path, docURI)
}

// recordTree interns every schema in the pristine document rooted at s into the
// node-identity index and stores, under the id it assigns, the schema's JSON
// Pointer path within that document and doc, the document's URI. The paths and
// document URIs name ref-node locations for fallback consultations. A schema
// already indexed stops the walk, so an aliased or cyclic graph keeps the first
// location recorded for a node.
//
// It is the walk behind [inliner.recordDoc] and [inliner.recordNode], its only
// callers. The currency each demands is where the vetting invariant is stated,
// the way [schemaIndex.walk] sits behind [schemaIndex.extend].
func (in *inliner) recordTree(s *Schema, path, doc string) {
	if s == nil {
		return
	}

	id, indexed := in.index.intern(s)
	if indexed {
		return
	}

	in.grow()

	in.paths[id] = path
	in.docs[id] = doc

	for _, child := range SubschemaEntries(s) {
		in.recordTree(child.Schema, path+child.Pointer, doc)
	}
}

// grow extends every per-node slice to the index's current node count, called
// after each intern so a freshly assigned id is addressable. Slots for a new id
// start at their zero value (empty path/doc, not in flight, no memoized copy).
func (in *inliner) grow() {
	n := in.index.len()
	in.paths = growSlice(in.paths, n)
	in.docs = growSlice(in.docs, n)
	in.inflight = growSlice(in.inflight, n)
	in.memo = growSlice(in.memo, n)
}

// vetTarget mints the [schemavet.Node] currency [inliner.recordNode] demands
// for a resolved target the walk has not recorded yet. Every such target comes
// from the session's JSON-pointer fallback, which vets it through this same
// vetter before returning it, so the checks short-circuit on their visited
// sets and the call re-mints that proof rather than running a second pass.
// The error return covers a target that reaches here unchecked. Its violation
// travels as an ordinary expansion failure, wrapping [ErrRefResolve] at the
// referencing ref, where a [WithRefFallback] policy answers it as it answers
// any other target the ref cannot use. A substitute's violation is fatal
// instead, since there the policy would be answering itself.
func (in *inliner) vetTarget(target *Schema, doc, ptr string) (schemavet.Node, error) {
	node, err := in.vetter.Vet(target, doc+"#"+ptr)
	if err != nil {
		return schemavet.Node{}, fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	return node, nil
}

// internedID returns the node id the index assigned to s. Every caller relies
// on the id addressing that schema's own per-node slots (paths, docs, inflight,
// memo), so a miss -- a schema the record walk was expected to have interned
// but did not -- is an internal invariant violation. It is surfaced as an error
// rather than letting a zero id silently alias slot 0, the pristine root's
// bookkeeping.
func (in *inliner) internedID(s *Schema) (int, error) {
	id, ok := in.index.nodeID(s)
	if !ok {
		return 0, fmt.Errorf("%w: schema not interned in the node index", ErrRefInline)
	}

	return id, nil
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
	// A node reached a second time is already expanded in place, and both
	// positions read that one node. Expanding it again would splice its target
	// into the same allOf twice.
	if in.expanded[working] {
		return nil
	}

	in.expanded[working] = true

	// Self-contained copies to join the node's allOf after its children are
	// walked: a Draft 2020-12 $ref target, a fallback substitute for a
	// $dynamicRef, or both.
	var copies []*Schema

	if in.profile.dynamicRef && pristine.DynamicRef != "" {
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

	replace := !in.profile.honorRefSiblings || IsTrueSchema(&rest)

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

	// A target already indexed and currently in flight closes a reference cycle;
	// a not-yet-indexed target cannot be in flight.
	if id, ok := in.index.nodeID(target); ok && in.inflight[id] {
		in.cycleFallbacks++

		return in.substitute(pristine, path, ref, fmt.Errorf("%w: %q", ErrRefCycle, ref))
	}

	// A target materialized from an unknown (Extra) keyword via a JSON pointer
	// is a fresh schema record never walked, so it has no recorded path or
	// document. Seed it (idempotently) with its own document and pointer so a
	// nested ref failure reports where the target physically lives. A
	// fragment-only ref (empty targetDoc) shares the referencing node's
	// document, and the ref's own pointer fragment is the target's location --
	// the referring node's path would mislocate a nested ref failure.
	if _, ok := in.index.nodeID(target); !ok {
		if targetDoc == "" {
			targetDoc, targetPtr, err = in.fragmentTargetLocation(pristine, ref)
			if err != nil {
				return nil, err
			}
		}

		node, vetErr := in.vetTarget(target, targetDoc, targetPtr)
		if vetErr != nil {
			return in.substitute(pristine, path, ref, vetErr)
		}

		in.recordNode(node, targetPtr, targetDoc)
	}

	targetID, err := in.internedID(target)
	if err != nil {
		return nil, err
	}

	return in.inlineCopy(target, in.paths[targetID], true)
}

// fragmentTargetLocation seeds the recorded location for a target reached by a
// fragment-only ref from the pristine node: the node's containing document
// paired with the ref's pointer fragment, prefixed by the enclosing resource
// root's recorded location. The prefix matters inside an embedded $id
// resource, where the fragment resolves against the resource root rather than
// the document root; there the bare fragment is resource-relative and would
// mislocate the target within the containing document.
func (in *inliner) fragmentTargetLocation(pristine *Schema, ref string) (string, string, error) {
	pristineID, err := in.internedID(pristine)
	if err != nil {
		return "", "", err
	}

	doc, ptr := in.docs[pristineID], strings.TrimPrefix(ref, "#")

	// Recorded paths use decoded tokens; a still-encoded fragment (one
	// url.Parse could not canonicalize, e.g. a %2F separator escape) keeps the
	// raw spelling its splitting depends on.
	parsed, perr := url.Parse(ref)
	if perr == nil {
		if fragment, encoded := uriref.RawFragment(parsed); !encoded {
			ptr = fragment
		}
	}

	if res := in.session.ResolveRef(pristine, "#", in.fetchDoc); res.Target != nil {
		if rootID, ok := in.index.nodeID(res.Target); ok {
			doc, ptr = in.docs[rootID], in.paths[rootID]+ptr
		}
	}

	return doc, ptr, nil
}

// substituteLocator names the consultation a substitute answered: the
// reference that failed, and the document and JSON Pointer of the node bearing
// it. The [RefFailure] carried in those same three fields. The fallback
// authors a substitute rather than reading it from a document, so the
// substitute's own path prefix locates nothing the caller can look up. Naming
// the failing reference's site lets a caller find the consultation that
// produced the schema.
func substituteLocator(doc, path, ref string) string {
	return fmt.Sprintf("the substitute for %q at %s#%s", ref, doc, path)
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

	pristineID, err := in.internedID(pristine)
	if err != nil {
		return nil, err
	}

	action := in.fallback.ResolveRefFailure(in.runContext(),
		RefFailure{Document: in.docs[pristineID], Path: path, Ref: ref, Err: inlineErr})

	if action.kind == refActionPropagate {
		return nil, inlineErr
	}

	if action.kind == refActionDrop {
		return nil, nil //nolint:nilnil // The caller drops the reference keyword.
	}

	locator := substituteLocator(in.docs[pristineID], path, ref)

	cp, err := cloneCheckedSchema(action.substitute, locator)
	if err != nil {
		return nil, err
	}

	// The substitute enters resolution space as a document of its own, so the
	// run vets it as one, under the base URI in effect at the failing node.
	// The violation names the ref that triggered the fallback, since a caller
	// reading a bare sentinel cannot tell which substitute carried it, and the
	// wrap keeps the sentinel reachable. A bad substitute is fatal rather than
	// a second consultation, which would let a policy that always substitutes
	// loop.
	base := in.session.SchemaBase(pristine)

	subDoc, err := in.vetter.VetDoc(cp, "", base)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", locator, err)
	}

	// Register the substitute's $id/$anchor in the per-run fallback registries
	// rather than the shared ones, so its nested refs resolve without the
	// substitute outranking a real document. RegisterFallbackDocument instead
	// refuses a substitute whose own $id names a URI a real document already
	// holds, the same rule Compile applies to a fetched document. The wrap
	// names the ref whose fallback supplied it, as the vet failure above does.
	err = in.session.RegisterFallbackDocument(cp, base, fmt.Sprintf("the substitute for %q", ref))
	if err != nil {
		//nolint:wrapcheck // The claimant above already names the ref.
		return nil, err
	}

	// A substitute that re-bases via its own $id reports a nested ref failure
	// in its own document: it is the root of that document, so its nested
	// failure paths are seeded at "" to stay rooted there, mirroring
	// fetchDoc's record. With no re-basing $id the subtree keeps the failing
	// node's path and containing document -- the document, not the node's base
	// URI: inside an embedded $id resource the base is the resource's $id
	// while the node-rooted path lives in the containing document, and pairing
	// the two would mislocate the failure.
	seed, doc := path, in.docs[pristineID]
	if in.session.SchemaBase(cp) != base {
		seed, doc = "", in.session.SchemaBase(cp)
	}

	in.recordDoc(subDoc, seed, doc)

	return in.inlineCopy(cp, seed, false)
}

// inlineCopy returns a self-contained copy of the pristine target: a fresh
// clone whose refs are expanded by the same pristine-space resolution as the
// rest of the run, leaving the target itself untouched; path is the target's
// JSON Pointer location within its containing document, seeding the walk's
// path tracking. When memoize is set, the completed target is recorded so one
// referenced from several places is expanded once; every additional use clones
// the memoized copy so no two positions in the output share nodes. A copy
// truncated by the cycle fallback is never recorded, since the truncation
// belongs to this expansion's inflight stack, not to the target. A
// substitute-originated copy passes memoize false: its pointer is fresh and
// never resolved again, so memoizing it would only accumulate dead entries. The
// inflight set marks targets whose copy is still being built: a ref resolving
// to one means the expansion reached its own target, which only a reference
// cycle can cause.
func (in *inliner) inlineCopy(target *Schema, path string, memoize bool) (*Schema, error) {
	// The target was interned before inlineCopy runs (record seeds it at the
	// resolving ref, or it belongs to a document already recorded); the checked
	// lookup turns a violation of that invariant into an error instead of
	// silently reading and writing slot 0's memo and inflight entries.
	id, err := in.internedID(target)
	if err != nil {
		return nil, err
	}

	if memoized := in.memo[id]; memoized != nil {
		return cloneSchema(memoized), nil
	}

	in.inflight[id] = true
	defer func() { in.inflight[id] = false }()

	cp := cloneSchema(target)

	// The $schema dialect declaration belongs to a document, not to a
	// spliced sub-schema; the output keeps the root document's dialect.
	cp.Schema = ""

	cyclesBefore := in.cycleFallbacks

	err = in.walkPair(cp, target, path)
	if err != nil {
		return nil, err
	}

	// The identifier keywords likewise belong to the target's original
	// position. Splicing them along with the copy would re-declare the
	// target's $id resource at every splice position and duplicate its
	// $anchor/$dynamicAnchor names within one resource, a document stricter
	// consumers reject. The copy is self-contained -- no reference survives an
	// expansion -- so the names have nothing left to resolve. Stripping runs
	// after the walk so it covers identifiers nested anywhere in the copy,
	// not only the top-level node.
	stripIdentifiers(cp)

	// A copy whose expansion consulted the cycle fallback was truncated by the
	// inflight stack of this particular expansion; an expansion of the same
	// target from a cycle-free position must not inherit the truncation, so
	// the copy is returned without being memoized.
	if memoize && in.cycleFallbacks == cyclesBefore {
		in.memo[id] = cp

		// Clone on the first use too, so the memo entry is never aliased to a
		// position in the output tree. Every caller, first or later, then gets an
		// independent copy, and no downstream mutation of one placement can leak
		// into another through a shared memo node.
		return cloneSchema(cp), nil
	}

	// A non-memoized copy (a substitute, a $dynamicRef expansion, or a
	// cycle-truncated build) is freshly built here and never stored in the
	// memo or aliased anywhere, so it is returned directly: a second deep
	// clone would only duplicate work.
	return cp, nil
}

// stripIdentifiers clears $id, $anchor, and $dynamicAnchor from every node of
// a spliced copy's subtree. The names identify the target at its original
// position; a copy spliced elsewhere must not re-declare them (see
// [inliner.inlineCopy]). A copy reproduces the pointer graph of the document it
// was cloned from, so the walk carries the visited set an aliased document
// needs to reach each node once.
func stripIdentifiers(s *Schema) {
	stripIdentifiersFrom(s, map[*Schema]bool{})
}

// stripIdentifiersFrom is [stripIdentifiers]'s recursion, carrying the set of
// nodes already stripped.
func stripIdentifiersFrom(s *Schema, stripped map[*Schema]bool) {
	if stripped[s] {
		return
	}

	stripped[s] = true

	s.ID = ""
	s.Anchor = ""
	s.DynamicAnchor = ""

	for _, entry := range SubschemaEntries(s) {
		stripIdentifiersFrom(entry.Schema, stripped)
	}
}

// resolveTarget resolves the ref at the pristine node to its pristine target
// schema through the shared resolution core, the same [refresolve.Session] the
// validator resolves against. Fragment-only refs resolve within the enclosing
// document; other refs absolutize against the node's base URI, fetch the
// addressed document (served from the registry when already loaded, otherwise
// through [inliner.fetchDoc]), and evaluate any fragment against it. Every
// unresolvable form returns an error wrapping [ErrRefResolve].
//
// The closure walk fetches and vets every document the graph reaches before any
// expansion, so the registry already holds what most resolutions here need. The
// fetch runs only for a document the walk left out: one a substitute's own
// reference introduces, or one whose vetting failed under a [WithRefFallback]
// run, where fetchDoc registers nothing and replays the failure from the
// negative cache.
//
// It also returns the target's own containing-document URI and its JSON Pointer
// within that document, so a caller seeding paths for an otherwise-unrecorded
// target (one materialized from an unknown keyword) reports a nested failure in
// the document it physically lives in. A fragment-only ref returns an empty
// document, signaling the caller to use the referencing node's own document.
func (in *inliner) resolveTarget(node *Schema, ref string) (*Schema, string, string, error) {
	res := in.session.ResolveRef(node, ref, in.fetchDoc)
	if res.Target == nil {
		if res.Err != nil {
			//nolint:wrapcheck // fetchDoc and the session's fallback vet already wrap their errors with ErrRefResolve.
			return nil, "", "", res.Err
		}

		return nil, "", "", fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, ref)
	}

	return res.Target, res.DocumentURI, res.Fragment, nil
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

// vetProfile is the structural-vetting policy the run applies to every
// document it holds: the draft's own policy, plus the inert-$id flag when
// [WithRetrievalBase] is set. Under that option the resolution walk passes
// every $id over, registering no URI and rebasing no child, so the keyword
// addresses nothing and its domain is not the vetter's business; checking it
// would refuse the published-$id documents the option exists to inline.
func (in *inliner) vetProfile() schemavet.Profile {
	profile := in.profile.vetProfile()
	profile.InertIDs = in.retrievalBase

	return profile
}

// fallbackVet is the session's [refresolve.FallbackVet]: the structural check
// each JSON-pointer fallback target passes before the session registers it. A
// target carved out of raw JSON in an unknown keyword never passed through a
// document-level vet, so the check runs at materialization, minus the
// identifier pass, which needs a document base no pointer target carries. It
// runs on the run's own vetter, so [inliner.vetTarget] can re-mint the proof
// where the walk records the target. A violation wraps [ErrRefResolve], so it
// surfaces through the referencing ref exactly like a malformed-document
// violation.
func (in *inliner) fallbackVet(sc *Schema, locator string) (schemavet.Node, error) {
	node, err := in.vetter.Vet(sc, locator)
	if err != nil {
		return schemavet.Node{}, fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	return node, nil
}

// fetchDoc is the inliner's [refresolve.Fetch] closure: it fetches the document
// at baseURI through the configured resolver, registers a pristine copy under
// baseURI, and returns the copy. The copy is resolution space only and is never
// mutated; output material is cloned from it on demand.
//
// Its own $ids, anchors, and base URIs join the same registry, through the
// registration the validator's fetch runs. A nested $id resolving to a URI
// another document already holds is refused with [ErrIDCollision] rather than
// merged, so no precedence between the two is needed.
//
// A resolver miss or error is recorded in the session's per-run negative cache
// and replayed on later fetches of the same URI, so the resolver is consulted
// at most once per baseURI in a run even when a [WithRefFallback] policy
// continues past the failure and many nodes reference the same unresolvable
// URI. Unlike the validator's fetch it returns an error (not a plain miss) on
// failure, preserving the inliner's fail-on-first-unresolvable-ref behavior.
//
// A tolerant [inliner.walkClosure] fetches the whole reference closure through
// it before any expansion, and [inliner.resolveTarget] reaches it for a document
// the closure did not name, such as one a substitute's own reference
// introduces.
//
// A fetched document is structurally vetted before registration, through the
// same [schemavet.Vetter] policy the validator applies (see
// [inliner.vetProfile] for the one option that narrows it), so a remote
// carrying an invalid type name, a negative bound, or (under a draft that
// rejects it) the array form of items fails [Inline] with an error wrapping
// [ErrRefResolve] rather than being inlined into a malformed output schema.
// The check is recorded in the negative cache like the other failures, so it
// too is run at most once per baseURI in a run.
//
// The strict closure walk fetches through [inliner.prefetchDoc] instead, which
// leaves the check to the walk's per-document hook so the two engines report a
// violation in the same order. This closure serves the tolerant walk a
// [WithRefFallback] run drives, and every expansion-time fetch.
func (in *inliner) fetchDoc(baseURI string) (*Schema, error) {
	if in.resolver == nil {
		return nil, fmt.Errorf("%w: no resolver configured for %q", ErrRefResolve, baseURI)
	}

	cp, missed, err := fetchAndClone(in.runContext(), in.resolver, in.session, baseURI)
	if err != nil {
		return nil, err
	}

	if missed {
		// Unlike the validator's fetch, a miss is fatal for the inliner: there
		// is no fallback answer for an unresolvable non-fragment ref.
		return nil, fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, baseURI)
	}

	// The vet below runs between the fetch and the registration, so this
	// checks the collision without merging first. See
	// [refresolve.Session.CheckFetched] for why the order matters. Registering
	// here instead would leave a malformed remote resolvable and rob
	// [WithRefFallback] of the failure it answers.
	collErr := in.session.CheckFetched(cp, baseURI)
	if collErr != nil {
		in.session.RecordRemoteMiss(baseURI, collErr)

		return nil, fmt.Errorf("%w: %w", ErrRefResolve, collErr)
	}

	doc, vetErr := schemavet.NewVetter(in.vetProfile()).VetDoc(cp, baseURI+"#", baseURI)
	if vetErr != nil {
		in.session.RecordRemoteMiss(baseURI, vetErr)

		return nil, fmt.Errorf("%w: %w", ErrRefResolve, vetErr)
	}

	regErr := in.registerDoc(cp, baseURI)
	if regErr != nil {
		return nil, regErr
	}

	in.recordDoc(doc, "", in.session.SchemaBase(cp))

	return cp, nil
}

// registerDoc puts a fetched document into resolution space under the URI the
// fetch retrieved it from, along with its own $ids, anchors, and base URIs. It
// is the registration the validator's fetch runs, so a fetched document enters
// both engines through one path and neither can refuse an identifier collision
// the other accepts. Both of the inliner's fetch closures register here, so
// they cannot drift from each other either.
//
// A document claiming a $id or anchor another document already holds is
// refused with [ErrIDCollision] and registers nothing. This method records that
// failure in the negative cache like the other fetch failures, so a run reports
// it once however many references name the document.
func (in *inliner) registerDoc(cp *Schema, baseURI string) error {
	err := in.session.RegisterFetched(cp, baseURI)
	if err != nil {
		in.session.RecordRemoteMiss(baseURI, err)

		return fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	return nil
}

// prefetchDoc is the [refresolve.Fetch] closure the strict closure walk drives.
// It fetches and registers like [inliner.fetchDoc] but vets nothing, leaving
// that to the walk's per-document hook, which runs over the registry frontier
// in sorted-URI order. That ordering is the point. It is the order Compile vets
// in, so a graph carrying both a malformed document and a reference that
// resolves to nothing makes the two engines report the same failure. Vetting
// inside the fetch cannot reproduce it, because a fetch runs when a reference
// first names the document rather than when the frontier reaches it.
//
// It mirrors the validator's own compile-time fetch, which likewise registers
// without vetting and leaves the check to the walk.
func (in *inliner) prefetchDoc(baseURI string) (*Schema, error) {
	if in.resolver == nil {
		return nil, fmt.Errorf("%w: no resolver configured for %q", ErrRefResolve, baseURI)
	}

	cp, missed, err := fetchAndClone(in.runContext(), in.resolver, in.session, baseURI)
	if err != nil {
		return nil, err
	}

	if missed {
		return nil, fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, baseURI)
	}

	err = in.registerDoc(cp, baseURI)
	if err != nil {
		return nil, err
	}

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
