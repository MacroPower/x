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
	// 3986 path-merge to the opaque part. Registration and lookup share this
	// function, so the result stays symmetric. Absolute and fragment-only refs
	// resolve correctly through ResolveReference.
	if baseURL.Opaque != "" && refURL.Scheme == "" && refURL.Opaque == "" &&
		refURL.Host == "" && refURL.Path != "" {
		resolved := url.URL{
			Scheme:     baseURL.Scheme,
			Opaque:     mergeOpaquePath(baseURL.Opaque, refURL.Path),
			RawQuery:   refURL.RawQuery,
			ForceQuery: refURL.ForceQuery,
			Fragment:   refURL.Fragment,
		}

		return resolved.String()
	}

	return baseURL.ResolveReference(refURL).String()
}

// mergeOpaquePath merges a relative path ref into an opaque URI part using the
// RFC 3986 merge step, treating the opaque part as a path: the ref replaces
// everything after the final slash. With no slash, the opaque part is split on
// its final ':' instead (a URN's NID/NSS structure), so the namespace is
// preserved rather than discarded; only when neither delimiter is present does
// the ref replace the whole opaque part.
func mergeOpaquePath(base, ref string) string {
	if i := strings.LastIndex(base, "/"); i >= 0 {
		return base[:i+1] + ref
	}

	// A URN opaque part such as "example:root" carries no slash but is still
	// structured by ':'. Replacing only the final colon-delimited component
	// keeps the namespace identifier, so a relative ref resolves to the same
	// absolute URN a caller would write directly: urn:example:root + "sub"
	// yields urn:example:sub, not urn:sub. Registration and lookup share
	// ResolveURI, so this keeps a relative $id and the canonical absolute $ref
	// agreeing on one registry key.
	if i := strings.LastIndex(base, ":"); i >= 0 {
		return base[:i+1] + ref
	}

	return ref
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
