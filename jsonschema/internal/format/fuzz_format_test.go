package format_test

import (
	"testing"
)

// Rig 3, layer 1 -- robustness. The built-in format validators are hand-rolled
// parsers of RFC string grammars, the third historical fix cluster. This layer
// is cheap crash-hunting: for every registered format, FuzzFormat<Name> feeds an
// arbitrary string and asserts the validator neither panics nor returns a
// non-deterministic result (the same input must classify the same way twice).
// The seed corpus runs on every `go test`; `task go:fuzz` searches for crashes.
//
// A panic in the fuzz body fails the test automatically, so the body only has to
// call the validator; the idempotency check catches any hidden state or
// data-dependent nondeterminism in the parser.

func FuzzFormatDateTime(f *testing.F) {
	fuzzFormatRobust(f, "date-time", "2020-01-02T03:04:05Z", "2020-01-02t03:04:05z",
		"2020-01-02T03:04:05.5+01:00", "2020-13-02T03:04:05Z", "not-a-date", "")
}

func FuzzFormatDate(f *testing.F) {
	fuzzFormatRobust(f, "date", "2020-01-02", "2020-13-02", "0000-00-00", "2020-1-2", "")
}

func FuzzFormatTime(f *testing.F) {
	fuzzFormatRobust(f, "time", "03:04:05Z", "23:59:60Z", "03:04:05.5+01:00", "3:04:05Z", "24:00:00Z", "")
}

func FuzzFormatEmail(f *testing.F) {
	fuzzFormatRobust(f, "email", "user@example.com", "a@b", "no-at-sign", "@example.com", "user@", "")
}

func FuzzFormatIDNEmail(f *testing.F) {
	fuzzFormatRobust(f, "idn-email", "user@example.com", "宏@example.com", "user@例.jp", "bad", "")
}

func FuzzFormatHostname(f *testing.F) {
	fuzzFormatRobust(f, "hostname", "example.com", "a.b.c", "-bad.com", "xn--bad-.com",
		"toolong", "example.", "123.com", "")
}

func FuzzFormatIDNHostname(f *testing.F) {
	fuzzFormatRobust(f, "idn-hostname", "example.com", "例.jp", "xn--fsq.jp", "-bad", "")
}

func FuzzFormatURI(f *testing.F) {
	fuzzFormatRobust(f, "uri", "https://example.com/a?b=c#d", "urn:isbn:0451450523",
		"//relative", "http://[::1]/", "not a uri", "")
}

func FuzzFormatURIReference(f *testing.F) {
	fuzzFormatRobust(f, "uri-reference", "https://example.com", "/path/only", "../rel", "#frag", "")
}

func FuzzFormatURITemplate(f *testing.F) {
	fuzzFormatRobust(f, "uri-template", "http://example.com/{id}", "/a{/b}c", "{unclosed", "plain", "")
}

func FuzzFormatIRI(f *testing.F) {
	fuzzFormatRobust(f, "iri", "https://example.com/é", "urn:isbn:0451450523", "not an iri", "")
}

func FuzzFormatIRIReference(f *testing.F) {
	fuzzFormatRobust(f, "iri-reference", "https://example.com/é", "/réf", "#frag", "")
}

func FuzzFormatUUID(f *testing.F) {
	fuzzFormatRobust(f, "uuid", "00000000-0000-0000-0000-000000000000",
		"f47ac10b-58cc-4372-a567-0e02b2c3d479", "not-a-uuid", "F47AC10B-58CC-4372-A567-0E02B2C3D479", "")
}

func FuzzFormatIPv4(f *testing.F) {
	fuzzFormatRobust(f, "ipv4", "192.168.0.1", "255.255.255.255", "0.0.0.0", "256.0.0.1",
		"1.2.3", "::ffff:1.2.3.4", "")
}

func FuzzFormatIPv6(f *testing.F) {
	fuzzFormatRobust(f, "ipv6", "::1", "2001:db8::1", "fe80::1%eth0", "::ffff:1.2.3.4",
		"1:2:3:4:5:6:7:8", "not:ipv6", "")
}

func FuzzFormatJSONPointer(f *testing.F) {
	fuzzFormatRobust(f, "json-pointer", "", "/", "/a/b", "/a~0b", "/a~1b", "bad", "/a~2b")
}

func FuzzFormatRelativeJSONPointer(f *testing.F) {
	fuzzFormatRobust(f, "relative-json-pointer", "0", "1/a", "2#", "0/a~0b", "-1", "")
}

func FuzzFormatRegex(f *testing.F) {
	fuzzFormatRobust(f, "regex", "^[a-z]+$", `(foo)\1`, "foo(?=bar)", "[", "(unclosed", "")
}

func FuzzFormatDuration(f *testing.F) {
	fuzzFormatRobust(f, "duration", "P1Y2M3DT4H5M6S", "P1W", "PT1H", "P1Y", "1Y", "P", "P1Y2D", "")
}

// fuzzFormatRobust seeds the corpus and asserts the named validator is
// crash-free and deterministic: it must classify a given input identically on
// repeat. The f.Fuzz callback cannot call t.Parallel (the fuzzing framework
// forbids it).
func fuzzFormatRobust(f *testing.F, name string, seeds ...string) {
	f.Helper()

	fn := validator(f, name)

	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		first := fn(s) == nil
		second := fn(s) == nil

		if first != second {
			t.Fatalf("validator %q is non-deterministic on %q: %v then %v", name, s, first, second)
		}
	})
}
