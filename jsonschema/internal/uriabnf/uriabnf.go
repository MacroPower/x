// Package uriabnf recognizes the URI, URI-reference, IRI, and IRI-reference
// productions of RFC 3986 appendix A and RFC 3987 section 2.2 by descent over
// the grammar itself. It is a second implementation kept apart from
// internal/format, which validates the same formats through net/url plus
// corrections: an oracle that shares net/url's reading of the grammar would
// share its gaps. The package answers only whether a string matches a
// production, and offers nothing a caller could use to parse one.
package uriabnf

import (
	"strings"
	"unicode/utf8"
)

// URI reports whether s matches the RFC 3986 URI production: a scheme, a
// hierarchical part, and an optional query and fragment.
func URI(s string) bool { return grammar{}.reference(s, true) }

// URIReference reports whether s matches the RFC 3986 URI-reference
// production: a URI or a relative reference.
func URIReference(s string) bool { return grammar{}.reference(s, false) }

// IRI reports whether s matches the RFC 3987 IRI production.
func IRI(s string) bool { return grammar{iri: true}.reference(s, true) }

// IRIReference reports whether s matches the RFC 3987 IRI-reference
// production.
func IRIReference(s string) bool { return grammar{iri: true}.reference(s, false) }

// grammar selects the character sets: the URI sets, or the IRI sets that add
// ucschar to unreserved everywhere and iprivate to the query.
type grammar struct {
	iri bool
}

// reference recognizes URI-reference, or URI when absolute is set. The
// fragment and query split off first, since neither carries the delimiter
// that opens it, and the remainder is a scheme plus hier-part or a
// relative-part.
func (g grammar) reference(s string, absolute bool) bool {
	rest, fragment, hasFragment := strings.Cut(s, "#")
	if hasFragment && !g.run(fragment, g.queryChar) {
		return false
	}

	rest, query, hasQuery := strings.Cut(rest, "?")
	if hasQuery && !g.run(query, g.queryOrPrivateChar) {
		return false
	}

	scheme, hier, hasScheme := splitScheme(rest)
	if hasScheme {
		return isScheme(scheme) && g.hierPart(hier)
	}

	if absolute {
		return false
	}

	return g.relativePart(rest)
}

// splitScheme splits rest at its first colon when the text before the colon
// could be a scheme: a relative reference whose first segment carries a colon
// is refused by path-noscheme, so a colon ahead of any slash is a scheme
// delimiter or nothing.
func splitScheme(rest string) (string, string, bool) {
	i := strings.IndexByte(rest, ':')
	if i < 0 || strings.Contains(rest[:i], "/") {
		return "", rest, false
	}

	return rest[:i], rest[i+1:], true
}

// isScheme recognizes scheme = ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ).
func isScheme(s string) bool {
	if s == "" || !isAlpha(s[0]) {
		return false
	}

	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isAlpha(c) && !isDigit(c) && c != '+' && c != '-' && c != '.' {
			return false
		}
	}

	return true
}

// hierPart recognizes hier-part: "//" authority path-abempty,
// path-absolute, path-rootless, or path-empty.
func (g grammar) hierPart(s string) bool {
	if after, ok := strings.CutPrefix(s, "//"); ok {
		return g.authorityAndPath(after)
	}

	if s == "" {
		return true
	}

	// Path-absolute and path-rootless both start with a non-empty first
	// segment after any leading slash, and a leading "//" is the authority
	// form handled above, so every segment is a plain segment here.
	return g.segments(s, g.pchar)
}

// relativePart recognizes relative-part: "//" authority path-abempty,
// path-absolute, path-noscheme, or path-empty.
func (g grammar) relativePart(s string) bool {
	if after, ok := strings.CutPrefix(s, "//"); ok {
		return g.authorityAndPath(after)
	}

	if s == "" {
		return true
	}

	if s[0] == '/' {
		return g.segments(s, g.pchar)
	}

	// path-noscheme: the first segment is segment-nz-nc, which admits no
	// colon, and the rest are plain segments.
	first, rest, hasRest := strings.Cut(s, "/")
	if first == "" || !g.run(first, g.pcharNoColon) {
		return false
	}

	return !hasRest || g.segments("/"+rest, g.pchar)
}

// authorityAndPath recognizes authority path-abempty: the authority runs to
// the first slash, and the path is empty or begins with one.
func (g grammar) authorityAndPath(s string) bool {
	authority, path := s, ""
	if i := strings.IndexByte(s, '/'); i >= 0 {
		authority, path = s[:i], s[i:]
	}

	return g.authority(authority) && (path == "" || g.segments(path, g.pchar))
}

// segments recognizes a path as a run of segments separated by slashes. A
// path-absolute's leading slash yields an empty first segment, which
// *( "/" segment ) admits. A path-absolute with a second leading slash would
// be an empty segment-nz, but the callers route "//" to the authority form
// first, so it never reaches here.
func (g grammar) segments(path string, allow func(rune) bool) bool {
	for segment := range strings.SplitSeq(path, "/") {
		if !g.run(segment, allow) {
			return false
		}
	}

	return true
}

// authority recognizes authority = [ userinfo "@" ] host [ ":" port ]. Neither
// userinfo nor host admits "@", so the authority carries at most one.
func (g grammar) authority(s string) bool {
	userinfo, hostport, hasUserinfo := strings.Cut(s, "@")
	if !hasUserinfo {
		hostport = s
	} else if strings.Contains(hostport, "@") || !g.run(userinfo, g.userinfoChar) {
		return false
	}

	if bracketed, ok := strings.CutPrefix(hostport, "["); ok {
		literal, after, closed := strings.Cut(bracketed, "]")
		if !closed || !isIPLiteral(literal) {
			return false
		}

		return after == "" || isPort(after)
	}

	host := hostport
	if i := strings.LastIndexByte(hostport, ':'); i >= 0 {
		if !isPort(hostport[i:]) {
			return false
		}

		host = hostport[:i]
	}

	// Host = IP-literal / IPv4address / reg-name, and reg-name admits every
	// IPv4address spelling, so the reg-name run decides.
	return g.run(host, g.regNameChar)
}

// isPort recognizes ":" *DIGIT, colon included.
func isPort(s string) bool {
	if s == "" || s[0] != ':' {
		return false
	}

	for i := 1; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}

	return true
}

// isIPLiteral recognizes the text between the brackets of an IP-literal:
// IPv6address or IPvFuture.
func isIPLiteral(s string) bool {
	return isIPv6Address(s) || isIPvFuture(s)
}

// isIPvFuture recognizes "v" 1*HEXDIG "." 1*( unreserved / sub-delims / ":" ),
// with the v case-insensitive as every ABNF literal is.
func isIPvFuture(s string) bool {
	if len(s) < 2 || (s[0] != 'v' && s[0] != 'V') {
		return false
	}

	version, tail, ok := strings.Cut(s[1:], ".")
	if !ok || version == "" || tail == "" || !isHexRun(version) {
		return false
	}

	for i := range len(tail) {
		c := tail[i]
		if !isUnreserved(rune(c)) && !isSubDelim(rune(c)) && c != ':' {
			return false
		}
	}

	return true
}

// isIPv6Address recognizes the RFC 3986 IPv6address production. Written out,
// every alternative is eight 16-bit groups, with one "::" standing for one or
// more zero groups and an IPv4address allowed as the last two, so the check
// counts groups: exactly eight with no "::", at most seven with one.
func isIPv6Address(s string) bool {
	left, right, elided := strings.Cut(s, "::")
	if elided && strings.Contains(right, "::") {
		return false
	}

	if !elided {
		n, ok := ipv6Groups(s, true)

		return ok && n == 8
	}

	n := 0

	if left != "" {
		groups, ok := ipv6Groups(left, false)
		if !ok {
			return false
		}

		n += groups
	}

	if right != "" {
		groups, ok := ipv6Groups(right, true)
		if !ok {
			return false
		}

		n += groups
	}

	return n <= 7
}

// ipv6Groups counts the groups in a colon-separated run of h16 units, where
// the last unit may be an IPv4address counting as two when tail is set. It
// reports false on an empty or malformed unit.
func ipv6Groups(s string, tail bool) (int, bool) {
	units := strings.Split(s, ":")
	n := 0

	for i, unit := range units {
		if tail && i == len(units)-1 && isIPv4Address(unit) {
			n += 2

			continue
		}

		if unit == "" || len(unit) > 4 || !isHexRun(unit) {
			return 0, false
		}

		n++
	}

	return n, true
}

// isIPv4Address recognizes four dec-octets: a value from 0 to 255 written
// without a leading zero.
func isIPv4Address(s string) bool {
	octets := strings.Split(s, ".")
	if len(octets) != 4 {
		return false
	}

	for _, octet := range octets {
		if !isDecOctet(octet) {
			return false
		}
	}

	return true
}

// isDecOctet recognizes dec-octet: DIGIT / %x31-39 DIGIT / "1" 2DIGIT /
// "2" %x30-34 DIGIT / "25" %x30-35.
func isDecOctet(s string) bool {
	if s == "" || len(s) > 3 || (len(s) > 1 && s[0] == '0') {
		return false
	}

	value := 0

	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}

		value = value*10 + int(s[i]-'0')
	}

	return value <= 255
}

// run reports whether s is a sequence of allowed code points and pct-encoded
// triplets. Ill-formed UTF-8 is never a code point the grammar admits.
func (g grammar) run(s string, allow func(rune) bool) bool {
	for i := 0; i < len(s); {
		if s[i] == '%' {
			if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
				return false
			}

			i += 3

			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return false
		}

		if !allow(r) {
			return false
		}

		i += size
	}

	return true
}

// unreservedChar is unreserved, or iunreserved under the IRI grammar.
func (g grammar) unreservedChar(r rune) bool {
	return isUnreserved(r) || (g.iri && isUcschar(r))
}

// pchar is unreserved / pct-encoded / sub-delims / ":" / "@", with
// pct-encoded handled by run.
func (g grammar) pchar(r rune) bool {
	return g.unreservedChar(r) || isSubDelim(r) || r == ':' || r == '@'
}

// pcharNoColon is the segment-nz-nc character set: pchar without the colon.
func (g grammar) pcharNoColon(r rune) bool {
	return g.unreservedChar(r) || isSubDelim(r) || r == '@'
}

// queryChar is the fragment and query set: pchar / "/" / "?".
func (g grammar) queryChar(r rune) bool {
	return g.pchar(r) || r == '/' || r == '?'
}

// queryOrPrivateChar is the query set, plus iprivate under the IRI grammar,
// which admits it in the query alone.
func (g grammar) queryOrPrivateChar(r rune) bool {
	return g.queryChar(r) || (g.iri && isIprivate(r))
}

// userinfoChar is unreserved / pct-encoded / sub-delims / ":".
func (g grammar) userinfoChar(r rune) bool {
	return g.unreservedChar(r) || isSubDelim(r) || r == ':'
}

// regNameChar is unreserved / pct-encoded / sub-delims.
func (g grammar) regNameChar(r rune) bool {
	return g.unreservedChar(r) || isSubDelim(r)
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isHexRun(s string) bool {
	for i := range len(s) {
		if !isHexDigit(s[i]) {
			return false
		}
	}

	return s != ""
}

// isUnreserved recognizes ALPHA / DIGIT / "-" / "." / "_" / "~".
func isUnreserved(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '.', r == '_', r == '~':
		return true
	default:
		return false
	}
}

// isSubDelim recognizes the sub-delims set.
func isSubDelim(r rune) bool {
	switch r {
	case '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=':
		return true
	default:
		return false
	}
}

// isUcschar recognizes the RFC 3987 ucschar ranges: the Unicode planes minus
// the surrogates, the noncharacters, the private-use areas, and the tags
// block.
func isUcschar(r rune) bool {
	switch {
	case r >= 0xA0 && r <= 0xD7FF,
		r >= 0xF900 && r <= 0xFDCF,
		r >= 0xFDF0 && r <= 0xFFEF:
		return true
	case r >= 0x10000 && r <= 0xDFFFD:
		// Planes 1 through 13, each ending short of its two noncharacters.
		return r&0xFFFF <= 0xFFFD
	case r >= 0xE1000 && r <= 0xEFFFD:
		return true
	default:
		return false
	}
}

// isIprivate recognizes the RFC 3987 iprivate ranges: the three private-use
// areas.
func isIprivate(r rune) bool {
	return (r >= 0xE000 && r <= 0xF8FF) || (r >= 0xF0000 && r <= 0xFFFFD) || (r >= 0x100000 && r <= 0x10FFFD)
}
