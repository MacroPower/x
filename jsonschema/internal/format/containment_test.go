package format_test

import (
	"strings"
	"testing"
)

// Rig 3, layer 2 -- cross-format containment invariants. Several of the
// built-in formats are grammar siblings: one's language is a subset of
// another's, or a mechanical rewrite carries a value from one language into the
// other. Those relationships need no oracle, so they are asserted directly as
// fuzz targets, and they are the layer that catches a check landing on only one
// side of a shared grammar -- the failure shape behind the idn-email domain
// divergence and the URI-authority divergence before it.
//
// Every target here is one-directional. The converse of a subset relation is
// not implied and must not be asserted.
//
// Deliberately not asserted: hostname implies idn-hostname. That containment is
// false by design. RFC 1123 §2.1 permits a label with a hyphen in positions 3
// and 4 ("ab--cd"), while RFC 5890 §2.3.2.2 makes it a reserved-LDH label that
// is invalid for IDNA, so idn-hostname rejects what hostname accepts. It is a
// documented deviation, not a defect; do not "restore" the invariant.

// FuzzContainmentURIImpliesURIReference asserts that every uri is a
// uri-reference. RFC 3986 §4.1: URI-reference = URI / relative-ref, so the uri
// language is a syntactic subset of the uri-reference language.
func FuzzContainmentURIImpliesURIReference(f *testing.F) {
	uri := validator(f, "uri")
	uriRef := validator(f, "uri-reference")

	addURISeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		if uri(s) != nil {
			return
		}

		err := uriRef(s)
		if err != nil {
			t.Fatalf("uri accepts %q but uri-reference rejects it: %v", s, err)
		}
	})
}

// FuzzContainmentURIImpliesIRI asserts that every uri is an iri. RFC 3987 §2.2
// derives the IRI grammar from RFC 3986's by widening iunreserved and the
// ipchar family with ucschar; every production a URI can use survives.
func FuzzContainmentURIImpliesIRI(f *testing.F) {
	uri := validator(f, "uri")
	iri := validator(f, "iri")

	addURISeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		if uri(s) != nil {
			return
		}

		err := iri(s)
		if err != nil {
			t.Fatalf("uri accepts %q but iri rejects it: %v", s, err)
		}
	})
}

// FuzzContainmentURIReferenceImpliesIRIReference asserts the same RFC 3987
// widening one level up, over the reference forms.
func FuzzContainmentURIReferenceImpliesIRIReference(f *testing.F) {
	uriRef := validator(f, "uri-reference")
	iriRef := validator(f, "iri-reference")

	addURISeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		if uriRef(s) != nil {
			return
		}

		err := iriRef(s)
		if err != nil {
			t.Fatalf("uri-reference accepts %q but iri-reference rejects it: %v", s, err)
		}
	})
}

// FuzzContainmentSchemedURIReferenceImpliesURI asserts the converse containment
// on the subset of references that carry a scheme: URI-reference = URI /
// relative-ref, and relative-ref cannot begin with a scheme (RFC 3986 §4.2), so
// an accepted reference whose prefix parses as "scheme:" must be an accepted
// uri. This is the direction that catches an authority-level check applied to
// only one of the two entry points.
func FuzzContainmentSchemedURIReferenceImpliesURI(f *testing.F) {
	uri := validator(f, "uri")
	uriRef := validator(f, "uri-reference")

	addURISeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		if uriRef(s) != nil || !hasURIScheme(s) {
			return
		}

		err := uri(s)
		if err != nil {
			t.Fatalf("uri-reference accepts schemed %q but uri rejects it: %v", s, err)
		}
	})
}

// hasURIScheme reports whether s begins with an RFC 3986 §3.1 scheme followed by
// ':' -- ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ). The colon must precede any
// '/', '?', or '#', because a colon inside a path segment, query, or fragment
// belongs to a relative reference ("./a:b") rather than to a scheme.
func hasURIScheme(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case c == ':':
			return i > 0

		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			// ALPHA is valid anywhere in a scheme.

		case c >= '0' && c <= '9', c == '+', c == '-', c == '.':
			// Valid only after the leading ALPHA.
			if i == 0 {
				return false
			}

		default:
			return false
		}
	}

	return false
}

// FuzzContainmentIPv4ImpliesMappedIPv6 asserts that every ipv4 address embeds in
// the IPv4-mapped IPv6 form. RFC 4291 §2.5.5.2 defines "::ffff:" + dotted-quad
// as an IPv6 address, and its dotted-quad tail is exactly the ipv4 grammar, so
// the two validators must agree on that tail.
func FuzzContainmentIPv4ImpliesMappedIPv6(f *testing.F) {
	ipv4 := validator(f, "ipv4")
	ipv6 := validator(f, "ipv6")

	addIPSeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		if ipv4(s) != nil {
			return
		}

		mapped := "::ffff:" + s

		err := ipv6(mapped)
		if err != nil {
			t.Fatalf("ipv4 accepts %q but ipv6 rejects %q: %v", s, mapped, err)
		}
	})
}

// FuzzContainmentJSONPointerImpliesRelative asserts that a json-pointer prefixed
// with a leading "0" is a relative-json-pointer. The relative-json-pointer draft
// defines relative-json-pointer = non-negative-integer ( "#" / json-pointer ),
// so the two share the pointer production verbatim.
func FuzzContainmentJSONPointerImpliesRelative(f *testing.F) {
	pointer := validator(f, "json-pointer")
	relative := validator(f, "relative-json-pointer")

	for _, seed := range []string{"", "/", "/a/b", "/a~0b", "/a~1b", "/a~2b", "bad", "/ ", "/é"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		if pointer(s) != nil {
			return
		}

		rel := "0" + s

		err := relative(rel)
		if err != nil {
			t.Fatalf("json-pointer accepts %q but relative-json-pointer rejects %q: %v", s, rel, err)
		}
	})
}

// FuzzContainmentDateTimeImpliesDateAndTime asserts that an accepted date-time
// decomposes into an accepted date and an accepted time. RFC 3339 defines
// date-time = full-date "T" full-time, and the three formats bind those exact
// productions, so a leniency reached through one entry point must be reachable
// through the other two.
func FuzzContainmentDateTimeImpliesDateAndTime(f *testing.F) {
	dateTime := validator(f, "date-time")
	date := validator(f, "date")
	timeOfDay := validator(f, "time")

	for _, seed := range []string{
		"2020-01-02T03:04:05Z", "2020-01-02t03:04:05z", "2020-01-02T03:04:05.5+01:00",
		"2020-12-31T23:59:60Z", "2020-13-02T03:04:05Z", "2020-01-02T03:04:05", "",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		if dateTime(s) != nil {
			return
		}

		// RFC 3339 permits a lowercase 't' designator, which the date-time
		// validator folds before splitting; splitting on either case here
		// reproduces the same boundary.
		sep := strings.IndexAny(s, "Tt")
		if sep < 0 {
			t.Fatalf("date-time accepts %q with no date/time separator", s)
		}

		head, tail := s[:sep], s[sep+1:]

		err := date(head)
		if err != nil {
			t.Fatalf("date-time accepts %q but date rejects its date half %q: %v", s, head, err)
		}

		err = timeOfDay(tail)
		if err != nil {
			t.Fatalf("date-time accepts %q but time rejects its time half %q: %v", s, tail, err)
		}
	})
}

// addURISeeds seeds the URI containment targets with absolute, relative,
// authority-bearing, and malformed forms, plus the colon-in-a-path-segment case
// that a naive scheme test misreads.
func addURISeeds(f *testing.F) {
	f.Helper()

	for _, seed := range []string{
		"https://example.com/a?b=c#d", "urn:isbn:0451450523", "mailto:a@b.example",
		"http://[::1]/", "http://[v7.x]/", "http://::1/a", "//example.com", "/path/only",
		"../sibling", "./a:b", "#frag", "?q=1", "", "http://exa mple.com", "a+b-c.d:x",
		"1abc:x", "https://example.com/é",
	} {
		f.Add(seed)
	}
}
