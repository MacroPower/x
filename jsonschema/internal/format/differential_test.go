package format_test

import (
	"net/mail"
	"net/netip"
	"net/url"
	"regexp/syntax"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Rig 3, layer 2 -- differential against stdlib oracles. No stdlib parser is a
// clean oracle for these validators (each is stricter or looser than stdlib by
// design), so every differential is framed as "agreement modulo a documented
// carve-out set": inputs that fall in a carve-out are skipped, and a mismatch
// outside the carve-outs is the finding.
//
// Each target states the one direction it asserts and why that direction is the
// sound one, because the two directions guard different failures and which one
// holds differs by oracle: against a looser oracle only "oracle accepts implies
// SUT accepts" is sound (the false-rejection guard), against a stricter one only
// "SUT accepts implies oracle accepts" (the false-acceptance guard). Never
// assert the converse of what a target documents.

// FuzzFormatIPv4VsNetip differentials the ipv4 validator against
// net/netip.ParseAddr + Is4. The SUT parses with net.ParseIP internally
// (format.go), which in modern Go is itself netip-backed, so the two agree on
// pure dotted-decimal IPv4. The oracle predicate is: netip.ParseAddr(s) succeeds
// and the address Is4 (a pure 4-byte address -- Is4 is false for an
// IPv4-in-IPv6 form like ::ffff:1.2.3.4, which the SUT also rejects for ipv4
// because it contains a colon).
func FuzzFormatIPv4VsNetip(f *testing.F) {
	fn := validator(f, "ipv4")
	addIPSeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		sutValid := fn(s) == nil

		addr, err := netip.ParseAddr(s)
		oracleValid := err == nil && addr.Is4()

		if sutValid == oracleValid {
			return
		}

		if ipCarveOut(s) {
			return
		}

		t.Fatalf(
			"ipv4 disagrees with netip.ParseAddr+Is4 on %q: sut=%v oracle=%v",
			s, sutValid, oracleValid,
		)
	})
}

// FuzzFormatIPv6VsNetip differentials the ipv6 validator against
// net/netip.ParseAddr + Is6. Oracle predicate: netip.ParseAddr(s) succeeds and
// the address Is6 (true for full IPv6 and IPv4-in-IPv6, false for pure IPv4).
// Carve-out: IPv6 zone IDs (fe80::1%eth0), which netip accepts but the SUT's
// net.ParseIP rejects, so a '%' in the input is skipped.
func FuzzFormatIPv6VsNetip(f *testing.F) {
	fn := validator(f, "ipv6")
	addIPSeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		sutValid := fn(s) == nil

		addr, err := netip.ParseAddr(s)
		oracleValid := err == nil && addr.Is6()

		if sutValid == oracleValid {
			return
		}

		if ipCarveOut(s) {
			return
		}

		t.Fatalf(
			"ipv6 disagrees with netip.ParseAddr+Is6 on %q: sut=%v oracle=%v",
			s, sutValid, oracleValid,
		)
	})
}

// ipCarveOut skips inputs where net.ParseIP (the SUT's parser) and net/netip
// split by construction. Zone IDs are the documented case: netip.ParseAddr
// accepts a zone (fe80::1%eth0) while net.ParseIP returns nil for it.
func ipCarveOut(s string) bool {
	return strings.Contains(s, "%")
}

// FuzzFormatDateTimeVsRFC3339 tests the normalization delta between the
// date-time validator and a bare time.Parse(time.RFC3339, s). The SUT calls
// time.Parse internally but first normalizes RFC 3339 leniencies that bare
// RFC3339 does not accept, and rejects a couple bare RFC3339 does accept. This
// is not an external oracle: it pins exactly that delta. Every normalized or
// rejected leniency is a carve-out; outside them the two must agree.
//
// Carve-outs, each a documented SUT-vs-RFC3339 difference:
//   - lowercase t/z designators: the SUT folds them, bare RFC3339 rejects;
//   - leap seconds (:60): the SUT accepts a valid one, RFC3339 rejects;
//   - the comma fractional-second separator: the SUT rejects it, RFC3339 accepts;
//   - a single-digit hour: the SUT requires a two-digit clock, time.Parse's "15"
//     field accepts one digit.
func FuzzFormatDateTimeVsRFC3339(f *testing.F) {
	fn := validator(f, "date-time")

	for _, seed := range []string{
		"2020-01-02T03:04:05Z", "2020-01-02T03:04:05+01:00", "2020-01-02T03:04:05.5Z",
		"2020-13-02T03:04:05Z", "2020-01-02T25:04:05Z", "2020-01-02T03:04:05",
		"not-a-date", "2020-01-02", "",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		sutValid := fn(s) == nil

		_, err := time.Parse(time.RFC3339, s)
		rfcValid := err == nil

		if sutValid == rfcValid {
			return
		}

		if dateTimeCarveOut(s) {
			return
		}

		t.Fatalf(
			"date-time disagrees with time.Parse(RFC3339) outside the normalization delta on %q: sut=%v rfc3339=%v",
			s, sutValid, rfcValid,
		)
	})
}

// dateTimeCarveOut reports whether s exercises one of the documented
// normalization leniencies, where the SUT and bare RFC3339 differ by design. The
// date half holds only digits and hyphens, so every leniency lives in the time
// half and the decomposition is shared with timeCarveOut.
func dateTimeCarveOut(s string) bool {
	// Lowercase t/z designators the SUT folds to uppercase. The 't' is checked
	// on the whole string because it is the separator the split below looks for.
	if strings.ContainsAny(s, "tz") {
		return true
	}

	_, timePart, ok := strings.Cut(s, "T")
	if !ok {
		return false
	}

	return timeCarveOut(timePart)
}

// timeCarveOut reports whether a time half exercises one of the documented
// leniencies. Each is a place the SUT and a bare time.Parse of the RFC 3339
// full-time layout differ by design.
func timeCarveOut(s string) bool {
	// The lowercase 'z' designator the SUT folds to uppercase.
	if strings.ContainsRune(s, 'z') {
		return true
	}

	// The comma fractional-second separator, which time.Parse accepts where a
	// fractional field is in play and the SUT rejects outright.
	if strings.Contains(s, ",") {
		return true
	}

	// A leap second the SUT accepts when valid but RFC 3339 rejects.
	if strings.Contains(s, ":60") {
		return true
	}

	// An out-of-range numeric zone offset. RFC 3339 bounds the offset's
	// time-hour to 00-23 and time-minute to 00-59, and the SUT enforces the
	// bounds, while time.Parse tolerates hour 24 and minute 60
	// ("...T00:00:00+24:00" parses).
	if offsetOutOfRange(s) {
		return true
	}

	// A single-digit hour. The SUT requires a two-digit clock; time.Parse's "15"
	// field accepts one digit.
	return !hasTwoDigitHour(s)
}

// offsetOutOfRange reports whether s ends in a numeric zone offset whose hour
// exceeds 23 or whose minute exceeds 59. It mirrors the SUT's offset
// decomposition (a trailing sign, two digits, a colon, two digits); anything
// else is not a numeric offset and is left to the other carve-outs.
func offsetOutOfRange(s string) bool {
	idx := strings.LastIndexAny(s, "+-")
	if idx < 0 || len(s)-idx != 6 {
		return false
	}

	off := s[idx:]
	if off[3] != ':' {
		return false
	}

	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	if !isDigit(off[1]) || !isDigit(off[2]) || !isDigit(off[4]) || !isDigit(off[5]) {
		return false
	}

	hour := int(off[1]-'0')*10 + int(off[2]-'0')
	minute := int(off[4]-'0')*10 + int(off[5]-'0')

	return hour > 23 || minute > 59
}

// hasTwoDigitHour reports whether timePart begins with two digits and a colon,
// the fixed-width hour the SUT requires. It classifies the carve-out for a
// single-digit hour; it is deliberately conservative (skipping is always safe).
func hasTwoDigitHour(timePart string) bool {
	if len(timePart) < 3 {
		return false
	}

	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }

	return isDigit(timePart[0]) && isDigit(timePart[1]) && timePart[2] == ':'
}

// FuzzFormatRegexVsRE2 differentials the regex validator against Go's
// regexp/syntax parser in Perl mode. The sound direction is one-way: RE2 parses
// s implies the regex format accepts s. The validator is deliberately looser
// than RE2 -- ECMA 262 permits backreferences and lookaround, which RE2 rejects
// by design -- so "SUT accepts implies RE2 parses" is false for a whole class of
// valid patterns and must not be asserted. What the one direction does guard is
// false rejection: a structural scan that misreads the grammar starts turning
// away patterns every engine compiles, which is how the Annex B identity-escape
// misread was found.
//
// Two carve-out families, each a genuine RE2/ECMA-262 divergence:
//   - \Q...\E literal quoting, an RE2/PCRE extension ECMA 262 does not have, so
//     RE2 reads "\Q[" as a literal '[' where ECMA 262 sees an unterminated class;
//   - "[]", where RE2 reads a leading ']' POSIX-style as a class member (making
//     "[]Z(]" a valid class) while ECMA 262 22.2.1 reads "[]" as the empty class.
func FuzzFormatRegexVsRE2(f *testing.F) {
	fn := validator(f, "regex")

	for _, seed := range []string{
		"^[a-z]+$", `(foo)\1`, "foo(?=bar)", "[abc]", "a{2,3}", `\a`, `\_`, `\c`,
		`\Q[\E`, "[]", "[]Z(]", "(", "[", `\`, "a|b", `\p{L}`, "",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		_, err := syntax.Parse(s, syntax.Perl)
		if err != nil {
			return
		}

		if regexCarveOut(s) {
			return
		}

		err = fn(s)
		if err != nil {
			t.Fatalf("RE2 parses %q but the regex format rejects it: %v", s, err)
		}
	})
}

// regexCarveOut skips patterns where RE2 and ECMA 262 read the same bytes as
// different constructs, so RE2 parsing one is no evidence ECMA 262 accepts it.
func regexCarveOut(s string) bool {
	// \Q...\E literal quoting suspends RE2's metacharacter reading; ECMA 262
	// has no such construct and keeps reading the quoted text as syntax.
	if strings.Contains(s, `\Q`) || strings.Contains(s, `\E`) {
		return true
	}

	// A leading ']' is a class member to RE2 and closes an empty class to
	// ECMA 262, so the two disagree on where the class ends.
	return strings.Contains(s, "[]")
}

// FuzzFormatURIVsNetURL differentials the uri validator against net/url. The
// sound direction is one-way: the format accepts s implies url.Parse(s) succeeds
// and the result IsAbs. This is the false-acceptance guard, which is the failure
// that matters for an assertion -- a caller who validates a string as a uri and
// then parses it must not have the parse fail. The converse is false by
// construction: url.Parse is permissive about characters RFC 3986 forbids, so it
// accepts plenty the format must reject.
//
// One carve-out: an IPvFuture authority (RFC 3986 §3.2.2), which net/url does
// not support and which reparseIPvFutureHost exists to accept.
func FuzzFormatURIVsNetURL(f *testing.F) {
	fn := validator(f, "uri")
	addURISeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		if fn(s) != nil || ipvFutureCarveOut(s) {
			return
		}

		u, err := url.Parse(s)
		if err != nil {
			t.Fatalf("uri accepts %q but url.Parse rejects it: %v", s, err)
		}

		if !u.IsAbs() {
			t.Fatalf("uri accepts %q but url.Parse reads no scheme", s)
		}
	})
}

// FuzzFormatURIReferenceVsNetURL differentials the uri-reference validator
// against net/url in the same false-acceptance direction, minus the scheme
// requirement: a reference may be relative (RFC 3986 §4.1). Same IPvFuture
// carve-out.
func FuzzFormatURIReferenceVsNetURL(f *testing.F) {
	fn := validator(f, "uri-reference")
	addURISeeds(f)

	f.Fuzz(func(t *testing.T, s string) {
		if fn(s) != nil || ipvFutureCarveOut(s) {
			return
		}

		_, err := url.Parse(s)
		if err != nil {
			t.Fatalf("uri-reference accepts %q but url.Parse rejects it: %v", s, err)
		}
	})
}

// ipvFutureCarveOut skips inputs carrying an IP-literal authority that could be
// IPvFuture. It is deliberately coarse -- any bracketed literal introduced by
// 'v' or 'V' -- since skipping is always safe and the precise production lives
// in isIPvFuture, which the test cannot reach.
func ipvFutureCarveOut(s string) bool {
	return strings.Contains(s, "[v") || strings.Contains(s, "[V")
}

// FuzzFormatEmailVsNetMail differentials the email validator against
// net/mail.ParseAddress. The sound direction is one-way: the format accepts s
// implies mail.ParseAddress(s) succeeds. The oracle parses RFC 5322 addr-spec,
// which is broader than the RFC 5321 Mailbox this format asserts (it takes CFWS
// and obs- productions), so the converse is false for a whole class of inputs
// and must not be asserted.
//
// Two carve-outs, both places net/mail is the stricter of the two:
//   - a quoted local part, where net/mail applies RFC 5322's quoted-string and
//     rejects the empty one RFC 5321's Quoted-string permits;
//   - a bracketed address literal, which net/mail rejects outright.
func FuzzFormatEmailVsNetMail(f *testing.F) {
	fn := validator(f, "email")

	for _, seed := range []string{
		"user@example.com", "john.doe@example.org", "a@b", `""@example.com`,
		`"a b"@example.com`, "user@[192.0.2.1]", "user@[IPv6:::1]", "user@123",
		"not-an-email", "@example.com", "user@", "",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		if fn(s) != nil || emailCarveOut(s) {
			return
		}

		_, err := mail.ParseAddress(s)
		if err != nil {
			t.Fatalf("email accepts %q but mail.ParseAddress rejects it: %v", s, err)
		}
	})
}

// emailCarveOut skips addresses whose local part is quoted or whose domain is a
// bracketed address literal, the two productions RFC 5321 has and net/mail's RFC
// 5322 reading does not accept.
func emailCarveOut(s string) bool {
	return strings.HasPrefix(s, `"`) || strings.Contains(s, "@[")
}

// FuzzFormatIDNHostnameVsIDNA differentials the idn-hostname validator against
// golang.org/x/net/idna. The sound direction here is the reverse of the URI
// targets: idna.Lookup.ToASCII converts every label implies idn-hostname accepts.
// The other direction is a theorem rather than a measurement, since the
// validator calls ToASCII itself and fails the label on error, so it could only
// ever catch a future refactor.
//
// What this exercises is the RFC 5890/5891 layer the validator adds on top of
// idna.Lookup, by requiring that layer to be the only thing narrowing it. Each
// check it adds is a carve-out: the numeric-TLD ban, the 63-octet A-label and
// 253-octet name caps, the trailing-dot and empty-label rules, the empty-A-label
// rule, and the RFC 5892 contextual-rule pass. This is narrower than importing
// Unicode's IdnaTestV2.txt, which would test whether that layer gives the right
// answer rather than that it is the only narrowing; see the note in
// testdata/idn_hostname_vectors.tsv.
func FuzzFormatIDNHostnameVsIDNA(f *testing.F) {
	fn := validator(f, "idn-hostname")

	for _, seed := range []string{
		"example.com", "a.b.c", "xn--fsq.jp", "münchen.de", "例え.jp", "αβγ.gr",
		"example.com.", "föö.example", "-bad.example", "xn--", "123", "a..b",
		"ab--cd.com", "l·l.example", "\u200cbad.example", "",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		if !idnaAcceptsHostname(s) {
			return
		}

		err := fn(s)
		if err != nil {
			t.Fatalf("idna.Lookup converts every label of %q but idn-hostname rejects it: %v", s, err)
		}
	})
}

// idnaAcceptsHostname is the oracle predicate with the carve-outs folded in:
// idna.Lookup.ToASCII converts every label of s, and s trips none of the RFC
// 5890/5891 checks the validator layers on top of it. A name failing either has
// nothing for the differential to assert. Each clause names the check it stands
// for; skipping is always safe, so the clauses are deliberately coarse.
func idnaAcceptsHostname(s string) bool {
	labels := splitIDNALabels(s)

	total := 0

	for i, label := range labels {
		// The empty-label rule, which also covers the trailing-dot rule (a root
		// dot is allowed only on a multi-label FQDN).
		if label == "" {
			return false
		}

		// An A-label takes the validator's ToUnicode pass before the contextual
		// rules, a second conversion the oracle does not make.
		if hasACEPrefixTest(label) {
			return false
		}

		// The RFC 5892 contextual-rule pass, which runs over the U-label and
		// rejects characters idna.Lookup admits.
		if strings.ContainsAny(label, contextualRunes) {
			return false
		}

		ascii, err := idna.Lookup.ToASCII(label)
		if err != nil {
			return false // the oracle itself will not convert this label
		}

		// The empty-A-label rule. A label of ignorable characters (a lone soft
		// hyphen, say) maps to nothing, which idna.Lookup reports as success.
		if ascii == "" {
			return false
		}

		// The 63-octet A-label cap.
		if len(ascii) > 63 {
			return false
		}

		total += len(ascii)
		if i > 0 {
			total++ // dot separator
		}
	}

	// The 253-octet name cap on the A-label form.
	if total > 253 {
		return false
	}

	// The RFC 1123 numeric-TLD ban, which IDNA has no equivalent of. Checked on
	// the mapped form, as the validator does, so a fullwidth-digit label is
	// caught too. Every label converted above, so this one converts.
	tld, err := idna.Lookup.ToASCII(labels[len(labels)-1])

	return err == nil && !isAllDigitsTest(tld)
}

// contextualRunes lists the code points checkContextualRules judges: the RFC
// 5892 Appendix A CONTEXTO characters and the DISALLOWED exceptions
// golang.org/x/net/idna does not itself reject.
const contextualRunes = "ـߺ〮〯〱〲〳〴〵〻" +
	"·͵׳״・"

// splitIDNALabels splits s on the four IDNA dot separators, mirroring
// splitIDNADots. It keeps empty labels, which are what the empty-label and
// trailing-dot carve-outs look for.
func splitIDNALabels(s string) []string {
	var labels []string

	start := 0

	for i, r := range s {
		if r == '.' || r == '。' || r == '．' || r == '｡' {
			labels = append(labels, s[start:i])
			start = i + utf8.RuneLen(r)
		}
	}

	return append(labels, s[start:])
}

// hasACEPrefixTest reports whether label carries the IDNA ACE prefix, mirroring
// hasACEPrefix. An A-label takes the validator's ToUnicode contextual pass,
// which is a carve-out.
func hasACEPrefixTest(label string) bool {
	return len(label) >= 4 && strings.EqualFold(label[:4], "xn--")
}

// isAllDigitsTest reports whether s is a non-empty run of ASCII digits,
// mirroring isAllDigits.
func isAllDigitsTest(s string) bool {
	if s == "" {
		return false
	}

	return strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// FuzzFormatDateVsTimeParse pins the date validator against time.Parse with the
// RFC 3339 full-date layout. There is no normalization delta to carve out: the
// validator is that call, and RFC 3339's full-date has no leniency to fold. The
// target exists so a future rewrite of the date path cannot silently move the
// boundary, which is the same role FuzzFormatDateTimeVsRFC3339 plays for the
// timestamp form.
func FuzzFormatDateVsTimeParse(f *testing.F) {
	fn := validator(f, "date")

	for _, seed := range []string{
		"2020-01-02", "2020-02-29", "2019-02-29", "2020-13-02", "2020-1-2",
		"20200102", "not-a-date", "",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		sutValid := fn(s) == nil

		_, err := time.Parse(time.DateOnly, s)
		oracleValid := err == nil

		if sutValid != oracleValid {
			t.Fatalf("date disagrees with time.Parse(DateOnly) on %q: sut=%v oracle=%v", s, sutValid, oracleValid)
		}
	})
}

// FuzzFormatTimeVsTimeParse is the time counterpart of
// FuzzFormatDateTimeVsRFC3339: it pins the normalization delta between the time
// validator and a bare time.Parse of the RFC 3339 full-time layout. The
// carve-outs are the same leniencies decomposed onto the time half, plus the
// fractional-second fallback, which the SUT reaches through a second layout the
// bare one does not have.
func FuzzFormatTimeVsTimeParse(f *testing.F) {
	fn := validator(f, "time")

	for _, seed := range []string{
		"03:04:05Z", "03:04:05z", "03:04:05+01:00", "03:04:05.5Z", "23:59:60Z",
		"23:59:60+00:30", "25:04:05Z", "3:04:05Z", "03:04:05", "03:04:05,5Z", "",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		sutValid := fn(s) == nil

		_, err := time.Parse(rfc3339FullTime, s)
		oracleValid := err == nil

		// A fractional second is a carve-out here and not in the date-time
		// target: RFC3339 has a fractional field and the bare full-time layout
		// does not, so only here does the SUT reach a second layout the oracle
		// has no equivalent of.
		if sutValid == oracleValid || strings.Contains(s, ".") || timeCarveOut(s) {
			return
		}

		t.Fatalf(
			"time disagrees with time.Parse(full-time) outside the normalization delta on %q: sut=%v oracle=%v",
			s, sutValid, oracleValid,
		)
	})
}

// rfc3339FullTime is the RFC 3339 full-time layout: the fixed-width clock plus a
// "Z" or "+hh:mm" offset, with no fractional-second field.
const rfc3339FullTime = "15:04:05Z07:00"

// addIPSeeds seeds the IP differentials with dotted-decimal, colon, mapped, and
// zoned forms plus malformed strings.
func addIPSeeds(f *testing.F) {
	f.Helper()

	for _, seed := range []string{
		"192.168.0.1", "255.255.255.255", "0.0.0.0", "256.0.0.1", "1.2.3", "01.02.03.04",
		"::1", "2001:db8::1", "fe80::1%eth0", "::ffff:1.2.3.4", "1:2:3:4:5:6:7:8",
		"not:an:address", "",
	} {
		f.Add(seed)
	}
}
