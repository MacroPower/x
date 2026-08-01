// Package uriref implements RFC 3986 URI-reference resolution and fragment
// handling for the $ref absolutization layer. It turns a relative $ref and an
// enclosing base URI into the single absolute key under which a schema both
// registers and is looked up, so registration and resolution agree. Resolution
// also corrects [net/url.ResolveReference] for an opaque base (a URN): the
// standard library collapses a relative ref against an opaque URI into a bogus
// authority form, so an opaque/URN merge applies the RFC 3986 path-merge to the
// opaque part instead. The fragment helpers strip, classify, and recover the
// raw (still percent-encoded) fragment a JSON Pointer needs.
package uriref

import (
	"bytes"
	"net/url"
	"strings"
)

// IsFragmentOnly reports whether a URI is fragment-only (e.g. "#foo").
func IsFragmentOnly(uri string) bool {
	return strings.HasPrefix(uri, "#")
}

// ResolveURI resolves ref against base per RFC 3986.
func ResolveURI(base, ref string) string {
	if base == "" {
		return ref
	}

	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}

	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}

	// The ResolveReference call mishandles an opaque base (a URN such as
	// urn:example:foo): a relative, non-fragment ref against it collapses to a
	// bogus authority form like "urn:///bar". An opaque URI has no hierarchical
	// path to merge, so resolve a relative non-fragment ref by applying the RFC
	// 3986 path-merge to the opaque part; a rooted ref path replaces the base
	// path outright per RFC 3986 section 5.2.2, spelled with OmitHost so the
	// result matches what url.Parse yields for the same absolute URI written
	// directly (urn:/c, not urn:///c). Registration and lookup share this
	// function, so the result stays symmetric. Absolute and fragment-only refs
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
	escaped := removeDotSegments(refURL.EscapedPath())

	resolved.RawPath = escaped
	resolved.OmitHost = true

	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		decoded = escaped
	}

	resolved.Path = decoded

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
	// ResolveURI, so this keeps a relative $id and the canonical absolute $ref
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

// StripFragment removes the fragment component from a URI.
func StripFragment(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return uri
	}

	parsed.Fragment = ""
	parsed.RawFragment = ""

	return parsed.String()
}

// AnchorKey returns the registry key for an anchor name declared within base,
// the base URI joined to the name by a fragment separator. A $ref to "#name"
// against the same base resolves to this identical key, so anchors register and
// resolve symmetrically.
func AnchorKey(base, name string) string {
	return base + "#" + name
}

// IDBase returns the canonical registry key for a hierarchical (non
// fragment-only) $id declared within base: the $id resolved against base per
// RFC 3986 with any fragment stripped. The result is both the key the schema
// registers under and the enclosing base for its sub-schemas, so a relative
// $id and the absolute $ref that targets it compute the same key.
func IDBase(base, id string) string {
	return StripFragment(ResolveURI(base, id))
}

// NormalizeBaseURI returns the canonical absolute form of a configured base
// URI. A base with no URI scheme is a file path; resolving it against
// file:/// makes RFC 3986 joining well-defined and gives the root document a
// registry key that refs absolutizing back to it reproduce exactly. An
// empty, absolute, or unparsable base passes through unchanged.
func NormalizeBaseURI(base string) string {
	if base == "" {
		return base
	}

	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "" {
		return base
	}

	return ResolveURI("file:///", base)
}

// RawFragment returns the JSON Pointer fragment to resolve plus whether it is
// still percent-encoded. The [url.Parse] result populates RawFragment only when
// the fragment carries an encoding it could not canonicalize (e.g. a %2F
// separator escape); that form must be split before decoding. Otherwise
// Fragment is already the single-decoded value and must not be decoded again.
func RawFragment(u *url.URL) (string, bool) {
	if u.RawFragment != "" {
		return u.RawFragment, true
	}

	return u.Fragment, false
}

// FilePathFromURI maps a ref URI to the file-system path it names. It drops a
// file:// scheme and any authority via [url.Parse] so file://host/x, file:///x,
// and file:////x all map to the path "x"; TrimPrefix alone mishandled an
// authority and extra leading slashes. Non-file and relative inputs fall back to
// the prior strip so they address the fs as before. It is the inverse of the
// file:/// base registration that [NormalizeBaseURI] performs.
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
