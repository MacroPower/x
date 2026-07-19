package format_test

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

// Rig 3, layer 2 -- differential against stdlib oracles. No stdlib parser is a
// clean oracle for these validators (each is stricter or looser than stdlib by
// design), so every differential is framed as "agreement modulo a documented
// carve-out set": inputs that fall in a carve-out are skipped, and a mismatch
// outside the carve-outs is the finding.

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
// normalization leniencies, where the SUT and bare RFC3339 differ by design.
func dateTimeCarveOut(s string) bool {
	// Lowercase t/z designators the SUT folds to uppercase.
	if strings.ContainsAny(s, "tz") {
		return true
	}

	// The comma fractional-second separator: RFC3339 (time.Parse) accepts it,
	// the SUT rejects it.
	if strings.Contains(s, ",") {
		return true
	}

	// A leap second the SUT accepts when valid but RFC3339 rejects.
	if strings.Contains(s, ":60") {
		return true
	}

	// A single-digit hour. The SUT requires a two-digit clock; time.Parse's "15"
	// field accepts one digit. Detect an hour field (the two characters after the
	// date/time separator) that is not two digits followed by a colon.
	if sep := strings.IndexByte(s, 'T'); sep >= 0 && !hasTwoDigitHour(s[sep+1:]) {
		return true
	}

	return false
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
