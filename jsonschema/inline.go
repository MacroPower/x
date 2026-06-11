package jsonschema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
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
// changing — or removing — what a later ref's JSON Pointer or anchor
// addresses.
type inliner struct {
	resolver RefResolver

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

	baseURI string
}

// InlineOption configures [Inline].
type InlineOption func(*inliner)

// WithInlineResolver sets the [RefResolver] used by [Inline] to fetch the
// documents that non-local refs target. The resolver receives the
// fragment-stripped absolute URI and is called at most once per distinct
// URI within one Inline call; the schema it returns is deep-copied before
// use and never mutated.
//
// Inline calls ResolveRef even when the resolver also implements
// [RefResolverContext]; context-aware inlining can be added when a consumer
// needs it.
func WithInlineResolver(r RefResolver) InlineOption {
	return func(in *inliner) { in.resolver = r }
}

// WithInlineBaseURI sets the base URI of the root document: the base that
// non-local refs in the root document absolutize against when no enclosing
// $id establishes one, exactly as a root $id would. Any fragment on base is
// ignored.
//
// A base with no URI scheme is taken as a file path and normalized against
// file:/// ("main.json" becomes "file:///main.json"), so RFC 3986 reference
// joining is well-defined and a ref in a fetched document that absolutizes
// back to the root resolves to the in-memory document instead of
// re-fetching it. [FileResolver] strips the file:// scheme and the leading
// "/", so [io/fs] paths keep working; a custom resolver paired with a
// schemeless base receives the normalized file:/// form.
func WithInlineBaseURI(base string) InlineOption {
	return func(in *inliner) { in.baseURI = stripFragment(base) }
}

// normalizeBaseURI returns the canonical absolute form of a configured base
// URI. A base with no URI scheme is a file path; resolving it against
// file:/// makes RFC 3986 joining well-defined and gives the root document a
// registry key that refs absolutizing back to it reproduce exactly. An
// empty, absolute, or unparsable base passes through unchanged.
func normalizeBaseURI(base string) string {
	if base == "" {
		return base
	}

	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "" {
		return base
	}

	return resolveURI("file:///", base)
}

// Inline returns a deep copy of s in which every $ref is replaced by a copy
// of the schema it targets, producing a self-contained schema. S and
// resolver-returned schemas are never mutated. A nil s returns nil.
//
// Fragment-only refs (#/pointer, #anchor) resolve within the enclosing
// document using the same $id/$anchor registry the validator builds. Other
// refs are absolutized against the enclosing resource's base URI (its $id,
// or the base from [WithInlineBaseURI], with a schemeless base normalized
// against file:///) and fetched through the resolver given via
// [WithInlineResolver]; any fragment is then evaluated against the fetched
// document. Fetched documents are inlined recursively using their own base
// URIs, and each document is fetched at most once per Inline call. Every
// ref resolves against its document's original structure, exactly as the
// validator would, so expanding one ref never changes what a later ref's
// JSON Pointer or anchor addresses.
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
// Refs are inlined only in the typed sub-schema positions [Subschemas]
// covers; a $ref carried as raw JSON inside an unknown keyword is left
// as-is, although a ref pointing into such a position still resolves.
//
// A ref whose expansion reaches its own target returns an error wrapping
// [ErrRefCycle]. A $dynamicRef under Draft 2020-12 has no faithful static
// expansion and returns an error wrapping [ErrRefInline] (Draft 7 ignores
// the keyword, as the validator does). A non-local ref with no resolver, or
// an unresolvable target, returns an error wrapping [ErrRefResolve].
func Inline(s *Schema, opts ...InlineOption) (*Schema, error) {
	if s == nil {
		return nil, nil //nolint:nilnil // A nil schema inlines to nil.
	}

	in := &inliner{
		inflight: map[*Schema]bool{},
		memo:     map[*Schema]*Schema{},
	}

	for _, opt := range opts {
		opt(in)
	}

	in.baseURI = normalizeBaseURI(in.baseURI)

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
	// mutation.
	in.v = &validator{
		root:                  pristine,
		draft:                 detectDraft(pristine),
		uriRegistry:           map[string]*Schema{},
		anchorRegistry:        map[string]*Schema{},
		dynamicAnchorRegistry: map[string]*Schema{},
		baseURIs:              map[*Schema]string{},
		walked:                map[*Schema]bool{},
	}
	in.v.walkSchema(pristine, in.baseURI)

	// Register the root document under its base URI when its own $id did
	// not already claim one, so a ref that absolutizes back to the root
	// document resolves to this copy instead of re-fetching it.
	if in.baseURI != "" {
		if _, ok := in.v.uriRegistry[in.baseURI]; !ok {
			in.v.uriRegistry[in.baseURI] = pristine
		}
	}

	err = in.walkPair(working, pristine)
	if err != nil {
		return nil, err
	}

	// A root that was itself a ref node may have been replaced wholesale by
	// a target copy, which never carries $schema; the returned document
	// keeps the input's dialect.
	working.Schema = s.Schema

	return working, nil
}

// walkPair makes working's subtree self-contained in place, reading all
// structure from its pristine counterpart. The two trees are clones of the
// same document and [Subschemas] returns children in deterministic order, so
// the walk pairs nodes position by position. A $ref is resolved against
// pristine structure, its target's self-contained copy is built by
// inlineCopy, and the copy is spliced into working per the draft's sibling
// rules. Spliced copies have no pristine counterpart and are already
// self-contained, so the walk never descends into them.
func (in *inliner) walkPair(working, pristine *Schema) error {
	if in.v.draft == Draft2020 && pristine.DynamicRef != "" {
		return fmt.Errorf("%w: $dynamicRef %q has no static expansion", ErrRefInline, pristine.DynamicRef)
	}

	var targetCopy *Schema

	if pristine.Ref != "" {
		tc, replace, err := in.expand(pristine)
		if err != nil {
			return err
		}

		if replace {
			// Draft-07 ignores siblings of $ref, so the node is replaced by
			// the target copy alone; a bare ref (no siblings) is replaced
			// directly under either draft. The copy is self-contained and
			// the node's pristine children no longer correspond to anything
			// in working, so the walk stops here.
			*working = *tc

			return nil
		}

		targetCopy = tc
	}

	workingChildren := Subschemas(working)
	pristineChildren := Subschemas(pristine)

	for i, p := range pristineChildren {
		err := in.walkPair(workingChildren[i], p)
		if err != nil {
			return err
		}
	}

	// Draft 2020-12 evaluates $ref alongside its siblings as a conjunction.
	// Keeping the siblings in place and joining the target copy to the
	// node's allOf preserves that: every assertion still applies, and
	// annotations from the target still surface at the node for the
	// unevaluated* keywords, which moving the siblings into a separate
	// allOf branch would break. The copy joins after the children are
	// walked so the child lists stay paired during the walk.
	if targetCopy != nil {
		working.Ref = ""
		working.AllOf = append(working.AllOf, targetCopy)
	}

	return nil
}

// expand resolves the $ref at the pristine node and returns a self-contained
// copy of its target, plus whether the draft's sibling rules call for
// replacing the ref node wholesale (Draft 7, or a node whose only keyword is
// $ref) rather than joining the copy to the node's allOf.
func (in *inliner) expand(pristine *Schema) (*Schema, bool, error) {
	ref := pristine.Ref

	target, err := in.resolveTarget(pristine, ref)
	if err != nil {
		return nil, false, err
	}

	if in.inflight[target] {
		return nil, false, fmt.Errorf("%w: %q", ErrRefCycle, ref)
	}

	tc, err := in.inlineCopy(target)
	if err != nil {
		return nil, false, err
	}

	rest := *pristine
	rest.Ref = ""

	replace := in.v.draft == Draft7 || IsTrueSchema(&rest)

	return tc, replace, nil
}

// inlineCopy returns a self-contained copy of the pristine target: a fresh
// clone whose refs are expanded by the same pristine-space resolution as the
// rest of the run, leaving the target itself untouched. Completed targets
// are memoized so one referenced from several places is expanded once; every
// additional use clones the memoized copy so no two positions in the output
// share nodes. The inflight set marks targets whose copy is still being
// built — a ref resolving to one means the expansion reached its own target,
// which only a reference cycle can cause.
func (in *inliner) inlineCopy(target *Schema) (*Schema, error) {
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

	err = in.walkPair(cp, target)
	if err != nil {
		return nil, err
	}

	in.memo[target] = cp

	return cp, nil
}

// resolveTarget resolves the ref at the pristine node to its pristine target
// schema. Fragment-only refs resolve within the enclosing document through
// the shared registries; other refs absolutize against the node's base URI,
// fetch the addressed document (served from the registry when already
// loaded), and evaluate any fragment against it. Every unresolvable form
// returns an error wrapping [ErrRefResolve].
func (in *inliner) resolveTarget(node *Schema, ref string) (*Schema, error) {
	if isFragmentOnly(ref) {
		target := in.v.resolveRef(node, ref)
		if target == nil {
			return nil, fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, ref)
		}

		return target, nil
	}

	base := in.v.schemaBase(node)
	absRef := resolveURI(base, ref)

	parsed, err := url.Parse(absRef)
	if err != nil {
		return nil, fmt.Errorf("%w: parse %q: %w", ErrRefResolve, absRef, err)
	}

	fragment := parsed.Fragment
	rawFrag, encoded := rawFragment(parsed)
	parsed.Fragment = ""
	parsed.RawFragment = ""
	baseURI := parsed.String()

	docRoot, ok := in.v.lookupURI(baseURI)
	if !ok {
		docRoot, err = in.fetchDoc(baseURI)
		if err != nil {
			return nil, err
		}
	}

	if fragment == "" {
		return docRoot, nil
	}

	// JSON Pointer within the fetched document. Pass the still-encoded
	// fragment so a member name escaped as %2F is not mistaken for a
	// pointer separator.
	if strings.HasPrefix(fragment, "/") {
		target := in.v.resolveJSONPointer(docRoot, rawFrag, encoded)
		if target == nil {
			return nil, fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, ref)
		}

		return target, nil
	}

	// Anchor within the fetched document.
	if target, ok := in.v.lookupAnchor(baseURI + "#" + fragment); ok {
		return target, nil
	}

	return nil, fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, ref)
}

// fetchDoc fetches the document at baseURI through the configured resolver,
// registers a pristine copy in the shared registries (walking it with
// baseURI as its base, so its $ids, anchors, and base URIs resolve like the
// root document's), and returns the copy. The copy is resolution space only
// and is never mutated; output material is cloned from it on demand.
func (in *inliner) fetchDoc(baseURI string) (*Schema, error) {
	if in.resolver == nil {
		return nil, fmt.Errorf("%w: no resolver configured for %q", ErrRefResolve, baseURI)
	}

	s, err := in.resolver.ResolveRef(baseURI)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	if s == nil {
		return nil, fmt.Errorf("%w: cannot resolve %q", ErrRefResolve, baseURI)
	}

	cp, err := cloneSchema(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRefResolve, err)
	}

	in.v.uriRegistry[baseURI] = cp
	in.v.walkSchema(cp, baseURI)

	return cp, nil
}

// FileResolver returns a [RefResolver] that serves file-path and relative
// URIs from fsys, unmarshaling each referenced file as a JSON schema
// document. Pair [os.DirFS] with [WithInlineBaseURI] to inline schemas that
// reference each other by relative file path.
//
// A leading "file://" scheme and a leading "/" are stripped, so URIs are
// resolved relative to the fs root: relative refs absolutize against the
// normalized base URI into file URIs (base "main.json" plus ref
// "sub/child.json" yields "file:///sub/child.json"), which strip back to
// paths addressing fsys from its root. The remaining path is used verbatim
// as the [io/fs] file name.
func FileResolver(fsys fs.FS) RefResolver {
	return fileResolver{fsys: fsys}
}

// fileResolver implements the [RefResolver] returned by [FileResolver].
type fileResolver struct {
	fsys fs.FS
}

// ResolveRef reads and unmarshals the schema document stored at the file
// path named by uri. See [FileResolver] for the path semantics.
func (r fileResolver) ResolveRef(uri string) (*Schema, error) {
	name := strings.TrimPrefix(uri, "file://")
	name = strings.TrimPrefix(name, "/")

	data, err := fs.ReadFile(r.fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read schema document: %w", err)
	}

	var s Schema

	err = json.Unmarshal(data, &s)
	if err != nil {
		return nil, fmt.Errorf("decode schema document %q: %w", name, err)
	}

	return &s, nil
}
