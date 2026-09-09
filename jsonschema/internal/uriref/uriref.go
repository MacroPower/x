// Package uriref is the one mint for document identity in the reference
// machinery. A [DocKey] is the canonical, fragment-free URI of one document,
// and only [Resolve] and [ParseBase] produce one, so every table keyed on a
// document reads and writes the same spelling however the document was
// reached: a relative $ref merged into its base, a $id resolved against its
// parent, a configured base, or a retrieval URI. [Location] and [AnchorKey]
// name a position and an anchor within a document, and are the only
// producers of the "#" that joins a document to a fragment.
//
// Resolution follows RFC 3986 section 5, with two extensions the standard
// leaves undefined and one correction to [net/url]: a relative reference
// resolves against a bare-path base (a root with no configured base), an
// opaque base (a URN) takes the path merge on its opaque part rather than
// the bogus authority form [net/url.URL.ResolveReference] produces, and the
// result takes the syntax-based normalization of section 6.2.2: a lowercase
// scheme and host, uppercase percent-encoding hex digits, no
// percent-encoding of an unreserved character, and no dot segments. Two
// spellings section 6.2.3 leaves to each scheme, an empty path and "/", stay
// distinct.
package uriref

import (
	"bytes"
	"net/url"
	"strings"
)

// DocKey is the canonical identity of one document: its URI, normalized and
// stripped of any fragment. The zero DocKey is the root document of a run
// with no base, whose locations and anchors spell as a bare "#". Only
// [Resolve] and [ParseBase] mint a non-zero key.
type DocKey struct {
	uri string
}

// String returns the canonical URI, "" for the zero key.
func (k DocKey) String() string { return k.uri }

// IsZero reports whether the key is the root document with no base.
func (k DocKey) IsZero() bool { return k.uri == "" }

// IsAbsolute reports whether the key names an absolute URI, one carrying a
// scheme. A relative $id resolved against no base leaves a key that is not,
// which registers no target a reference can absolutize back to.
func (k DocKey) IsAbsolute() bool {
	if k.uri == "" {
		return false
	}

	u, err := url.Parse(k.uri)

	return err == nil && u.IsAbs()
}

// At names the position the JSON Pointer addresses within the document.
func (k DocKey) At(pointer string) Location {
	return Location{Doc: k, Pointer: pointer}
}

// Anchor names an anchor declared within the document.
func (k DocKey) Anchor(name string) AnchorKey {
	return AnchorKey{Doc: k, Name: name}
}

// Location is a position within a document: the document and the RFC 6901
// JSON Pointer to the node, "" for the root.
type Location struct {
	Doc     DocKey
	Pointer string
}

// String spells the location as the document's URI, "#", and the pointer:
// "#" for the root of the base-less root document, "#/a" for a node of it,
// and "uri#/a" for a node of a keyed document.
func (l Location) String() string {
	return l.Doc.uri + "#" + l.Pointer
}

// AnchorKey is an anchor name within a document, the key an $anchor,
// $dynamicAnchor, or Draft-07 fragment $id registers under and a "#name"
// reference resolves to.
type AnchorKey struct {
	Doc  DocKey
	Name string
}

// String spells the key as the document's URI, "#", and the name.
func (a AnchorKey) String() string {
	return a.Doc.uri + "#" + a.Name
}

// Fragment is the fragment a reference carries, in the two forms a resolver
// reads: the decoded text an anchor is named by, and the raw text a JSON
// Pointer is split on, which stays percent-encoded when [net/url] could not
// canonicalize it (a %2F separator escape must be split before it decodes).
type Fragment struct {
	decoded string
	raw     string
	encoded bool
}

// fragmentOf reads the fragment off a parsed URI.
func fragmentOf(u *url.URL) Fragment {
	if u.RawFragment != "" {
		return Fragment{decoded: u.Fragment, raw: u.RawFragment, encoded: true}
	}

	return Fragment{decoded: u.Fragment, raw: u.Fragment}
}

// IsEmpty reports a reference with no fragment, or an empty one.
func (f Fragment) IsEmpty() bool { return f.decoded == "" && f.raw == "" }

// IsPointer reports a JSON Pointer fragment, one whose decoded form starts
// with "/". The decoded form is what settles it: a fragment whose leading
// separator is a percent-escaped "%2F" is still a pointer, and its raw text
// is split on both spellings downstream.
func (f Fragment) IsPointer() bool { return strings.HasPrefix(f.decoded, "/") }

// Name returns the decoded text, the name an anchor reference carries.
func (f Fragment) Name() string { return f.decoded }

// Pointer returns the text a JSON Pointer is split on and whether it is
// still percent-encoded.
func (f Fragment) Pointer() (string, bool) { return f.raw, f.encoded }

// IsFragmentOnly reports whether a reference is fragment-only (e.g. "#foo").
func IsFragmentOnly(uri string) bool {
	return strings.HasPrefix(uri, "#")
}

// Resolve resolves ref against the document base per RFC 3986 and returns
// the document the result names, canonicalized, with the fragment it
// carries. A fragment-only reference names base itself. A reference that
// does not parse is an error.
//
// A base with no scheme and no authority is a bare path, the form a root
// document with no configured base and every document fetched through it
// carry. RFC 3986 defines resolution only against an absolute base, so this
// function extends it: a relative ref merges into the base path per the
// section 5.2.3 merge and takes remove_dot_segments, keeping the unrooted
// shape the base had. The zero base takes remove_dot_segments alone. Both
// keep a document on one key however it is reached, so "dir/a.json" named
// from the root and "a.json" named from "dir/b.json" resolve to the same key
// and the document is fetched once.
func Resolve(base DocKey, ref string) (DocKey, Fragment, error) {
	refURL, err := url.Parse(ref)
	if err != nil {
		//nolint:wrapcheck // The caller names the keyword the reference came from.
		return DocKey{}, Fragment{}, err
	}

	if IsFragmentOnly(ref) {
		return base, fragmentOf(refURL), nil
	}

	resolved, err := url.Parse(resolveURI(base.uri, refURL))
	if err != nil {
		//nolint:wrapcheck // The caller names the keyword the reference came from.
		return DocKey{}, Fragment{}, err
	}

	fragment := fragmentOf(resolved)

	return canonical(resolved), fragment, nil
}

// ParseBase mints the key of a configured base: a base with no scheme is a
// file path and resolves against file:///, so RFC 3986 joining is
// well-defined and a reference absolutizing back to the root reproduces the
// key exactly; a Windows drive path ("C:\schemas\main.json" or
// "C:/schemas/main.json") is the file URI RFC 8089 appendix E.2 gives it,
// "file:///C:/schemas/main.json", with the drive letter's case kept; an
// absolute base is canonicalized; and a fragment on any of them is dropped.
// The empty base is the zero key. A base that is not a URI reference is an
// error, so it cannot corrupt every key derived from it.
func ParseBase(base string) (DocKey, error) {
	if base == "" {
		return DocKey{}, nil
	}

	if isDrivePath(base) {
		base = "file:///" + strings.ReplaceAll(base, "\\", "/")
	}

	parsed, err := url.Parse(base)
	if err != nil {
		//nolint:wrapcheck // The caller names the option the value came from.
		return DocKey{}, err
	}

	if parsed.Scheme == "" {
		parsed, err = url.Parse(resolveURI("file:///", parsed))
		if err != nil {
			//nolint:wrapcheck // The caller names the option the value came from.
			return DocKey{}, err
		}
	}

	return canonical(parsed), nil
}

// isDrivePath reports whether base starts with a Windows drive letter and a
// colon. The net/url parser reads "C:/x" as the scheme "c" and "C:\x" as an
// opaque URI, so the check runs on the raw text before parsing. No registered
// URI scheme is one letter long, so a one-letter scheme can only be a drive.
func isDrivePath(base string) bool {
	if len(base) < 2 || base[1] != ':' {
		return false
	}

	c := base[0]

	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// canonical applies the RFC 3986 section 6.2.2 syntax-based normalization
// and drops the fragment: the scheme and host lowercase, every
// percent-encoded octet uppercase and decoded where it is unreserved, and
// the path free of dot segments. The opaque part of a URN takes the
// percent-encoding rule alone, since it has no path to normalize.
func canonical(u *url.URL) DocKey {
	c := *u
	c.Scheme = strings.ToLower(c.Scheme)
	c.Host = strings.ToLower(canonicalEscapes(c.Host))
	c.Fragment = ""
	c.RawFragment = ""

	if c.RawQuery != "" {
		c.RawQuery = canonicalEscapes(c.RawQuery)
	}

	if c.Opaque != "" {
		c.Opaque = canonicalEscapes(c.Opaque)

		return DocKey{uri: c.String()}
	}

	setPath(&c, removeDotSegments(canonicalEscapes(c.EscapedPath())))

	return DocKey{uri: c.String()}
}

// canonicalEscapes rewrites every percent-encoded octet of s in its
// canonical form: decoded where it encodes an unreserved character, and
// with uppercase hex digits otherwise. A malformed triplet is left as it is.
func canonicalEscapes(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}

	var b strings.Builder

	b.Grow(len(s))

	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
			b.WriteByte(s[i])

			continue
		}

		octet := unhex(s[i+1])<<4 | unhex(s[i+2])
		if isUnreserved(octet) {
			b.WriteByte(octet)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperHex(s[i+1]))
			b.WriteByte(upperHex(s[i+2]))
		}

		i += 2
	}

	return b.String()
}

// isUnreserved reports an RFC 3986 unreserved character, which a canonical
// URI never percent-encodes.
func isUnreserved(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '.', c == '_', c == '~':
		return true
	default:
		return false
	}
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func upperHex(c byte) byte {
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 'A'
	}

	return c
}

// setPath stores an escaped path on u so that [url.URL.String] emits it
// verbatim: RawPath carries the spelling and Path its decoded form.
func setPath(u *url.URL, escaped string) {
	u.RawPath = escaped

	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		decoded = escaped
	}

	u.Path = decoded
}

// resolveURI resolves a parsed reference against a base URI string per RFC
// 3986, before canonicalization.
func resolveURI(base string, refURL *url.URL) string {
	if base == "" {
		if isRelativePathRef(refURL) {
			return relativePathRef(refURL, removeDotSegments(refURL.EscapedPath()))
		}

		return refURL.String()
	}

	baseURL, err := url.Parse(base)
	if err != nil {
		return refURL.String()
	}

	if baseURL.Scheme == "" && baseURL.Host == "" && baseURL.Opaque == "" &&
		!strings.HasPrefix(baseURL.EscapedPath(), "/") && isRelativeRef(refURL) {
		return resolveBarePathRef(baseURL, refURL)
	}

	// The ResolveReference call mishandles an opaque base (a URN such as
	// urn:example:foo): a relative, non-fragment ref against it collapses to a
	// bogus authority form like "urn:///bar". An opaque URI has no hierarchical
	// path to merge, so resolve a relative non-fragment ref by applying the RFC
	// 3986 path-merge to the opaque part; a rooted ref path replaces the base
	// path outright per RFC 3986 section 5.2.2, spelled with OmitHost so the
	// result matches what url.Parse yields for the same absolute URI written
	// directly (urn:/c, not urn:///c). Absolute and fragment-only refs
	// resolve correctly through ResolveReference.
	// The merge operates on the encoded path form: url.URL.Opaque is emitted
	// verbatim by String() and kept raw by url.Parse, so feeding it the decoded
	// Path would resolve a percent-escaped ref to a different key than its
	// absolute spelling (and emit invalid URIs for escapes like %20). The
	// hierarchical branch resolves on escaped paths the same way. RawFragment
	// is carried along so a still-encoded JSON Pointer fragment keeps the raw
	// spelling its splitting depends on.
	if baseURL.Opaque != "" && refURL.Scheme == "" && refURL.Opaque == "" &&
		refURL.Host == "" && refURL.Path != "" {
		return resolveOpaqueRef(baseURL, refURL)
	}

	return baseURL.ResolveReference(refURL).String()
}

// isRelativeRef reports whether a parsed reference carries no scheme, no
// authority, and no rooted path: a relative-path reference, or a query- or
// fragment-only one. Such a reference resolves against its base's path
// rather than replacing it.
func isRelativeRef(u *url.URL) bool {
	return u.Scheme == "" && u.Host == "" && u.Opaque == "" && u.User == nil &&
		!strings.HasPrefix(u.EscapedPath(), "/")
}

// isRelativePathRef reports whether a relative reference carries a path,
// which merges with the base's rather than keeping it.
func isRelativePathRef(u *url.URL) bool {
	return isRelativeRef(u) && u.Path != ""
}

// resolveBarePathRef resolves a relative ref against a bare-path base (no
// scheme, no authority, unrooted path) per RFC 3986 section 5.2.2: a ref with
// a path takes the section 5.2.3 merge (the base's last segment dropped, the
// ref path appended) and remove_dot_segments, in the base's unrooted shape; a
// ref with no path keeps the base path and takes the base query unless it
// carries one. The fragment is the ref's either way.
func resolveBarePathRef(baseURL, refURL *url.URL) string {
	basePath := baseURL.EscapedPath()

	if refURL.Path == "" {
		out := *refURL
		if out.RawQuery == "" && !out.ForceQuery {
			out.RawQuery, out.ForceQuery = baseURL.RawQuery, baseURL.ForceQuery
		}

		return relativePathRef(&out, basePath)
	}

	merged := refURL.EscapedPath()
	if i := strings.LastIndex(basePath, "/"); i >= 0 {
		merged = basePath[:i+1] + merged
	}

	return relativePathRef(refURL, removeDotSegments(merged))
}

// relativePathRef spells a relative-path reference from its canonical path
// and the query and fragment of the reference it came from.
func relativePathRef(refURL *url.URL, path string) string {
	out := url.URL{
		RawQuery:    refURL.RawQuery,
		ForceQuery:  refURL.ForceQuery,
		Fragment:    refURL.Fragment,
		RawFragment: refURL.RawFragment,
	}

	setPath(&out, path)

	return out.String()
}

// resolveOpaqueRef resolves a relative non-fragment ref against an opaque
// base: a rooted ref path replaces the base path outright per RFC 3986
// section 5.2.2, anything else merges into the opaque part.
func resolveOpaqueRef(baseURL, refURL *url.URL) string {
	resolved := url.URL{
		Scheme:      baseURL.Scheme,
		RawQuery:    refURL.RawQuery,
		ForceQuery:  refURL.ForceQuery,
		Fragment:    refURL.Fragment,
		RawFragment: refURL.RawFragment,
	}

	if !strings.HasPrefix(refURL.Path, "/") {
		resolved.Opaque = mergeOpaquePath(baseURL.Opaque, refURL.EscapedPath())

		return resolved.String()
	}

	// A rooted path skips the merge and replaces the base path, with the same
	// remove_dot_segments step 5.2.2 prescribes. RawPath keeps the
	// still-encoded spelling String() must emit, mirroring the encoded-form
	// discipline of the merge branch.
	setPath(&resolved, removeDotSegments(refURL.EscapedPath()))

	resolved.OmitHost = true

	return resolved.String()
}

// mergeOpaquePath merges a relative path ref into an opaque URI part using the
// RFC 3986 merge step, treating the opaque part as a path: the ref replaces
// everything after the final slash. With no slash, the opaque part is split on
// its final ':' instead (a URN's NID/NSS structure), so the namespace is
// preserved rather than discarded; only when neither delimiter is present does
// the ref replace the whole opaque part. RFC 3986 5.2.2 follows the merge with
// remove_dot_segments, which the hierarchical branch gets from
// [url.URL.ResolveReference]; applying it here keeps a dot-segmented ref and
// its canonical absolute spelling on one registry key. The URN NID prefix
// (everything through the last ':' before the first '/') stays out of segment
// popping so ".." cannot consume the namespace identifier.
func mergeOpaquePath(base, ref string) string {
	if i := strings.LastIndex(base, "/"); i >= 0 {
		merged := base[:i+1] + ref

		prefix := ""
		if j := strings.LastIndex(merged[:strings.Index(merged, "/")+1], ":"); j >= 0 {
			prefix, merged = merged[:j+1], merged[j+1:]
		}

		return prefix + removeDotSegments(merged)
	}

	// A URN opaque part such as "example:root" carries no slash but is still
	// structured by ':'. Replacing only the final colon-delimited component
	// keeps the namespace identifier, so a relative ref resolves to the same
	// absolute URN a caller would write directly: urn:example:root + "sub"
	// yields urn:example:sub, not urn:sub. Registration and lookup share
	// Resolve, so this keeps a relative $id and the canonical absolute $ref
	// agreeing on one registry key.
	if i := strings.LastIndex(base, ":"); i >= 0 {
		return base[:i+1] + removeDotSegments(ref)
	}

	return removeDotSegments(ref)
}

// removeDotSegments applies the RFC 3986 5.2.4 remove_dot_segments algorithm.
// A path with no leading slash keeps that shape: the algorithm's output always
// starts segments with '/', so a leading slash the input never had is trimmed
// back off.
func removeDotSegments(path string) string {
	rooted := strings.HasPrefix(path, "/")

	var out []byte

	for path != "" {
		switch {
		case strings.HasPrefix(path, "../"):
			path = path[len("../"):]
		case strings.HasPrefix(path, "./"):
			path = path[len("./"):]
		case strings.HasPrefix(path, "/./"):
			path = path[len("/."):]
		case path == "/.":
			path = "/"
		case strings.HasPrefix(path, "/../"), path == "/..":
			if path == "/.." {
				path = "/"
			} else {
				path = path[len("/.."):]
			}

			if i := bytes.LastIndexByte(out, '/'); i >= 0 {
				out = out[:i]
			} else {
				out = out[:0]
			}

		case path == "." || path == "..":
			path = ""
		default:
			// Move the first segment, including its leading '/' if present, to
			// the output.
			end := len(path)
			if i := strings.IndexByte(path[1:], '/'); i >= 0 {
				end = i + 1
			}

			out = append(out, path[:end]...)
			path = path[end:]
		}
	}

	if !rooted {
		return strings.TrimPrefix(string(out), "/")
	}

	return string(out)
}

// FilePathFromURI maps a ref URI to the file-system path it names. It drops a
// file:// scheme and any authority via [url.Parse] so file://host/x, file:///x,
// and file:////x all map to the path "x"; TrimPrefix alone mishandled an
// authority and extra leading slashes. Non-file and relative inputs fall back to
// the prior strip so they address the fs as before. It is the inverse of the
// file:/// base registration that [ParseBase] performs. A drive path
// ("file:///C:/schemas/x.json") maps to "C:/schemas/x.json", which is
// absolute and not a name an [os.DirFS] serves, so a caller serving such a
// base from a directory strips the "file:///C:/schemas/" prefix first.
func FilePathFromURI(uri string) string {
	u, err := url.Parse(uri)
	if err == nil && u.Scheme == "file" {
		// An opaque file: URI (file:schema.json, with no authority slashes)
		// puts the whole reference in u.Opaque and leaves u.Path empty; fall
		// back to it so the filename is not dropped.
		if u.Path == "" && u.Opaque != "" {
			// Percent-decode to match the decoding url.Parse already applies to
			// u.Path for the file:///x and file://host/x forms, so the same
			// filename maps to the same fs name regardless of authority slashes. A
			// malformed escape falls back to the literal rather than a garbage path.
			opaque := strings.TrimLeft(u.Opaque, "/")

			decoded, derr := url.PathUnescape(opaque)
			if derr == nil {
				return decoded
			}

			return opaque
		}

		return strings.TrimLeft(u.Path, "/")
	}

	// Relative refs use the parsed path so a query string or fragment does not
	// leak into the fs name, the same way the file branch drops them. A
	// non-empty, non-file scheme such as http or urn instead keeps the raw
	// strip, so it stays a non-fs string and misses rather than collapsing to a
	// plausible local path.
	if err == nil && u.Scheme == "" && u.Path != "" {
		return strings.TrimPrefix(u.Path, "/")
	}

	return strings.TrimPrefix(strings.TrimPrefix(uri, "file://"), "/")
}
