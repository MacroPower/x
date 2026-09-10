// Package format implements the built-in JSON Schema string-format
// validators (date-time, email, hostname, uri, uuid, ...). Each validator
// checks a single string value against its format's defining specification.
package format

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/text/secure/bidirule"
	"golang.org/x/text/unicode/bidi"
)

// Validators returns the built-in format validators keyed by JSON Schema
// format name. The returned map is shared; callers must not modify it.
func Validators() map[string]func(string) error {
	return builtinFormats
}

var (
	// Built-in format validators keyed by JSON Schema format name.
	builtinFormats = map[string]func(string) error{
		"date-time":             validateDateTime,
		"date":                  validateDate,
		"time":                  validateTime,
		"email":                 validateEmail,
		"idn-email":             validateIDNEmail,
		"hostname":              validateHostname,
		"idn-hostname":          validateIDNHostname,
		"uri":                   validateURI,
		"uri-reference":         validateURIReference,
		"uri-template":          validateURITemplate,
		"iri":                   validateIRI,
		"iri-reference":         validateIRIReference,
		"uuid":                  validateUUID,
		"ipv4":                  validateIPv4,
		"ipv6":                  validateIPv6,
		"json-pointer":          validateJSONPointer,
		"relative-json-pointer": validateRelativeJSONPointer,
		"regex":                 validateRegex,
		"duration":              validateDuration,
	}

	// Date designators in their RFC 3339 ABNF chain order
	// (dur-year = nY [dur-month], dur-month = nM [dur-day]). Components must
	// appear contiguously in this order; weeks (W) are a separate alternative
	// handled outside this chain.
	durationDateOrder = map[byte]int{'Y': 0, 'M': 1, 'D': 2}

	// Time designators in their RFC 3339 ABNF chain order
	// (dur-hour = nH [dur-minute], dur-minute = nM [dur-second]). Components
	// must appear contiguously in this order.
	durationTimeOrder = map[byte]int{'H': 0, 'M': 1, 'S': 2}

	// The rejections more than one site reports. The validator caller
	// renders each with %v, so the text is the message a user sees; sharing
	// one value per message keeps the sites in step and lets [errors.Is]
	// name a verdict.
	errInvalidHostname            = errors.New("invalid hostname")
	errInvalidUUID                = errors.New("invalid UUID")
	errInvalidTime                = errors.New("invalid time")
	errInvalidDateTime            = errors.New("invalid date-time")
	errInvalidTimeOffset          = errors.New("invalid time offset")
	errInvalidIPv4                = errors.New("invalid IPv4 address")
	errInvalidIPv6                = errors.New("invalid IPv6 address")
	errDurationNoComponents       = errors.New("invalid duration: no components")
	errRegexUnbalancedParenthesis = errors.New("invalid regex: unbalanced parenthesis")
	errRegexNothingToRepeat       = errors.New("invalid regex: nothing to repeat")
)

func validateDateTime(s string) error {
	upper := strings.ToUpper(s)

	// Split on T separator (RFC 3339 allows lowercase t). Uppercasing first
	// folds the lowercase 't' so the date and time halves split cleanly; the
	// date half holds only digits and hyphens, so uppercasing leaves it
	// unchanged before it is handed to validateDate.
	datePart, timePart, ok := strings.Cut(upper, "T")
	if !ok {
		return errInvalidDateTime
	}

	err := validateDate(datePart)
	if err != nil {
		return errInvalidDateTime
	}

	// The time half is already a substring of the uppercased timestamp, so
	// validate it without folding again.
	return validateTimeUpper(timePart)
}

func validateDate(s string) error {
	_, err := time.Parse("2006-01-02", s)
	if err != nil {
		return errors.New("invalid date")
	}

	return nil
}

func validateTime(s string) error {
	return validateTimeUpper(strings.ToUpper(s))
}

// validateTimeUpper validates an RFC 3339 partial-time whose lowercase 't'/'z'
// designators are already folded to uppercase. The date-time path folds the
// whole timestamp once and passes its already-folded time half here, avoiding a
// second fold; the standalone "time" format entry folds via validateTime.
func validateTimeUpper(upper string) error {
	// RFC 3339 partial-time requires a two-digit hour, minute, and second.
	// Go's time.Parse accepts a single-digit hour for the "15" layout field, so
	// enforce the fixed-width "hh:mm:ss" shape explicitly; this also keeps the
	// fixed byte offsets in validateLeapSecond aligned.
	if !hasTwoDigitClock(upper) {
		return errInvalidTime
	}

	// RFC 3339 time-secfrac permits only a period as the fractional-second
	// separator, but Go's time.Parse also accepts a comma. After the
	// fixed-width "hh:mm:ss" prefix, byte 8 is the only position a fractional
	// separator can occupy, so reject a comma there explicitly (before leap-
	// second normalization, so "23:59:60,5Z" is caught too).
	if len(upper) > 8 && upper[8] == ',' {
		return errInvalidTime
	}

	// Handle leap second: temporarily replace the seconds ":60" with ":59" for
	// parsing. The fixed-width "hh:mm:ss" prefix is guaranteed by
	// hasTwoDigitClock, so the seconds field is bytes [5:8]; anchoring the match
	// there avoids treating a ":60" in the trailing zone offset's minute field
	// (e.g. "12:30:45+00:60") as a leap second. Such an offset is independently
	// rejected by validateTimeOffset.
	isLeap := upper[5:8] == ":60"
	normalized := upper
	if isLeap {
		normalized = upper[:6] + "59" + upper[8:]
	}

	_, err := time.Parse("15:04:05Z07:00", normalized)
	if err != nil {
		_, err = time.Parse("15:04:05.999999999Z07:00", normalized)
	}

	if err != nil {
		return errInvalidTime
	}

	err = validateTimeOffset(upper)
	if err != nil {
		return err
	}

	// Leap seconds are only valid when the UTC time is 23:59.
	if isLeap {
		err := validateLeapSecond(upper)
		if err != nil {
			return err
		}
	}

	return nil
}

// hasTwoDigitClock reports whether s begins with a fixed-width "hh:mm:ss" clock,
// with two digits for each of the hour, minute, and second, as RFC 3339
// partial-time requires. It does not range-check the fields or inspect the
// optional fractional-second and offset parts.
func hasTwoDigitClock(s string) bool {
	if len(s) < 8 {
		return false
	}

	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }

	return isDigit(s[0]) && isDigit(s[1]) && s[2] == ':' &&
		isDigit(s[3]) && isDigit(s[4]) && s[5] == ':' &&
		isDigit(s[6]) && isDigit(s[7])
}

// validateLeapSecond verifies that a time with second=60 corresponds to
// 23:59 UTC by applying the time zone offset.
func validateLeapSecond(s string) error {
	hour := int(s[0]-'0')*10 + int(s[1]-'0')
	minute := int(s[3]-'0')*10 + int(s[4]-'0')

	utcMinutes := hour*60 + minute - utcOffsetMinutes(s)

	// Normalize to [0, 1440).
	utcMinutes = ((utcMinutes % 1440) + 1440) % 1440

	if utcMinutes != 23*60+59 {
		return errors.New("invalid time: leap second not at 23:59 UTC")
	}

	return nil
}

// offsetKind classifies the trailing zone designator of an RFC 3339 time.
type offsetKind int

const (
	// OffsetNone marks a "Z"/"z" zone or an absent designator: no numeric
	// offset to decompose, and a zero contribution when converting to UTC.
	offsetNone offsetKind = iota
	// OffsetMalformed marks a "+"/"-" designator that is not the required
	// "+hh:mm"/"-hh:mm" shape (six bytes with ':' at index 3).
	offsetMalformed
	// OffsetNumeric marks a well-formed "+hh:mm"/"-hh:mm" designator whose
	// sign, hour, and minute fields are populated.
	offsetNumeric
)

// timeOffset describes the trailing zone designator of an RFC 3339 time. The
// sign, hour, and minute fields are populated only when kind is offsetNumeric.
type timeOffset struct {
	kind   offsetKind
	sign   byte // '+' or '-'
	hour   int  // offset hours
	minute int  // offset minutes
}

// parseTimeOffset locates and decomposes the trailing RFC 3339 zone designator
// of s. It captures the shared byte parsing used by both utcOffsetMinutes and
// validateTimeOffset; it does not range-check the components, so each caller
// keeps its own semantics.
func parseTimeOffset(s string) timeOffset {
	if strings.HasSuffix(s, "Z") {
		return timeOffset{kind: offsetNone}
	}

	idx := strings.LastIndexAny(s, "+-")
	if idx < 0 {
		return timeOffset{kind: offsetNone}
	}

	offset := s[idx:]
	if len(offset) != 6 || offset[3] != ':' {
		return timeOffset{kind: offsetMalformed}
	}

	return timeOffset{
		kind:   offsetNumeric,
		sign:   offset[0],
		hour:   int(offset[1]-'0')*10 + int(offset[2]-'0'),
		minute: int(offset[4]-'0')*10 + int(offset[5]-'0'),
	}
}

// utcOffsetMinutes returns the signed minute offset encoded in a time string's
// trailing zone designator: positive for "+hh:mm", negative for "-hh:mm". A
// trailing "Z" or an absent/malformed offset yields 0. Converting a local time
// to UTC subtracts this value.
func utcOffsetMinutes(s string) int {
	off := parseTimeOffset(s)
	if off.kind != offsetNumeric {
		return 0
	}

	total := off.hour*60 + off.minute
	if off.sign == '-' {
		return -total
	}

	return total
}

// validateTimeOffset checks that a time zone offset has valid hour (<24) and
// minute (<60) components.
func validateTimeOffset(s string) error {
	off := parseTimeOffset(s)
	switch off.kind {
	case offsetNone:
		return nil
	case offsetMalformed:
		return errInvalidTimeOffset
	case offsetNumeric:
		if off.hour > 23 || off.minute > 59 {
			return errInvalidTimeOffset
		}
	}

	return nil
}

// maxEmailOctets is the RFC 5321 §4.5.3.1.3 size limit on a forward path: 256
// octets including the enclosing angle brackets, so 254 for the address itself.
// It does not follow from the local-part and domain limits, which sum to 318.
// RFC 6531 §3.4 carries the limit over to an internationalized address, counting
// octets there too.
const maxEmailOctets = 254

// validateEmail validates an email address against the RFC 5321 Mailbox grammar
// and the §4.5.3.1 size limits: 64 octets for the local part (§4.5.3.1.1), 253
// for the domain (§4.5.3.1.2, via validateHostnameLabels), and 254 for the
// forward path as a whole (§4.5.3.1.3). The total is not implied by the two
// parts, which sum to 318.
func validateEmail(s string) error {
	if len(s) > maxEmailOctets {
		return errors.New("invalid email: address length")
	}

	local, domain, ok := splitEmail(s)
	if !ok {
		return errors.New("invalid email")
	}

	err := validateEmailLocal(local)
	if err != nil {
		return err
	}

	return validateEmailDomain(domain)
}

// splitEmail splits an address into its local part and domain at the '@'
// separating them, honoring a quoted local part (which may itself contain '@').
func splitEmail(s string) (string, string, bool) {
	if s == "" {
		return "", "", false
	}

	if s[0] == '"' {
		// Quoted local part: scan to the closing unescaped quote.
		i := 1
		for i < len(s) {
			if s[i] == '\\' {
				i += 2
				continue
			}

			if s[i] == '"' {
				break
			}

			i++
		}

		if i >= len(s) || s[i] != '"' {
			return "", "", false // unterminated quoted string
		}

		if i+1 >= len(s) || s[i+1] != '@' {
			return "", "", false // quoted local part must be followed by '@'
		}

		return s[:i+1], s[i+2:], true
	}

	at := strings.IndexByte(s, '@')
	if at < 1 || at == len(s)-1 {
		return "", "", false
	}

	return s[:at], s[at+1:], true
}

// validateEmailLocal validates the local part of an email address (RFC 5321),
// accepting both dot-atom and quoted-string forms.
func validateEmailLocal(s string) error {
	if s == "" || len(s) > 64 {
		return errors.New("invalid email: local part length")
	}

	if s[0] == '"' {
		return validateQuotedLocal(s, false)
	}

	return validateDotAtomLocal(s)
}

// validateQuotedLocal validates a quoted-string local part per RFC 5321:
// qtextSMTP (%d32-33 / %d35-91 / %d93-126) runs interleaved with
// quoted-pairSMTP (%d92 %d32-126). The allowUnicode flag additionally admits
// well-formed non-ASCII UTF-8 runes in the unescaped text, the RFC 6531
// widening for idn-email; RFC 6531 extends only qtextSMTP, not
// quoted-pairSMTP, so the escaped character must be printable ASCII for both
// formats.
//
// The empty quoted local part ""@example.com is accepted: Quoted-string =
// DQUOTE *QcontentSMTP DQUOTE permits zero content characters (as does RFC
// 5322's quoted-string, which Draft-07 cites), and the format assertion is
// defined by grammar conformance, not deliverability.
func validateQuotedLocal(s string, allowUnicode bool) error {
	if len(s) < 2 || s[len(s)-1] != '"' {
		return errors.New("invalid email: malformed quoted local part")
	}

	inner := s[1 : len(s)-1]

	for i := 0; i < len(inner); {
		c := inner[i]
		switch {
		case c == '\\':
			// Quoted-pairSMTP = %d92 %d32-126. A backslash as the final
			// interior byte would have escaped the closing quote and cannot
			// reach here via splitEmail, but reject it defensively.
			if i+1 >= len(inner) || inner[i+1] < 0x20 || inner[i+1] > 0x7E {
				return errors.New("invalid email: invalid quoted-pair in local part")
			}

			i += 2

		case c >= 0x20 && c <= 0x7E && c != '"':
			// QtextSMTP: printable ASCII except '"'; '\\' is the case above.
			i++

		case c >= utf8.RuneSelf && allowUnicode:
			// RFC 6531 widens qtextSMTP with UTF8-non-ascii, which must be a
			// well-formed sequence; an ill-formed byte decodes to
			// (utf8.RuneError, 1) and is rejected.
			r, size := utf8.DecodeRuneInString(inner[i:])
			if r == utf8.RuneError && size == 1 {
				return errors.New("invalid email: malformed UTF-8 in local part")
			}

			i += size

		default:
			return errors.New("invalid email: invalid character in quoted local part")
		}
	}

	return nil
}

// dotAtomErrors carries the messages a dot-atom validation reports for each
// rejection. Callers that report leading/trailing and consecutive dots with a
// single message may set edgeDot and doubleDot to the same value.
type dotAtomErrors struct {
	edgeDot   string // leading or trailing dot
	doubleDot string // consecutive dots
	badChar   string // a rune that is neither a dot nor accepted by isText
}

// validateDotAtom validates a dot-atom local part: isText runs separated by
// single dots, with no leading, trailing, or consecutive dots. The isText
// predicate selects the permitted run characters (RFC 5321 atext for email,
// the wider IDN atext for idn-email), and msgs supplies the rejection messages.
func validateDotAtom(s string, isText func(rune) bool, msgs dotAtomErrors) error {
	if s[0] == '.' || s[len(s)-1] == '.' {
		return errors.New(msgs.edgeDot)
	}

	if strings.Contains(s, "..") {
		return errors.New(msgs.doubleDot)
	}

	for i := 0; i < len(s); {
		// Decode explicitly rather than ranging: RFC 6531 widens atext with
		// UTF8-non-ascii, which admits only well-formed sequences, and a range
		// loop folds an ill-formed byte into (utf8.RuneError, 1)
		// indistinguishably from a genuine U+FFFD. The quoted-local scan
		// rejects the same case.
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return errors.New(msgs.badChar)
		}

		if r != '.' && !isText(r) {
			return errors.New(msgs.badChar)
		}

		i += size
	}

	return nil
}

// validateDotAtomLocal validates a dot-atom local part: atext runs separated by
// single dots, with no leading, trailing, or consecutive dots.
func validateDotAtomLocal(s string) error {
	return validateDotAtom(s, isAtext, dotAtomErrors{
		edgeDot:   "invalid email: misplaced dot in local part",
		doubleDot: "invalid email: misplaced dot in local part",
		badChar:   "invalid email: invalid character in local part",
	})
}

// isAtext reports whether r is an RFC 5321 atom character (atext).
func isAtext(r rune) bool {
	if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
		return true
	}

	switch r {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '/', '=', '?', '^', '_', '`', '{', '|', '}', '~', '-':
		return true
	}

	return false
}

// validateEmailDomain validates the domain part: either a bracketed address
// literal ([IPv4] or [IPv6:...]) or a hostname.
func validateEmailDomain(d string) error {
	if d == "" {
		return errors.New("invalid email: empty domain")
	}

	if strings.HasPrefix(d, "[") && strings.HasSuffix(d, "]") {
		lit := d[1 : len(d)-1]
		// The "IPv6:" tag is an ABNF literal (RFC 5321 §4.1.3), so it is
		// case-insensitive per RFC 5234 §2.3.
		if len(lit) >= len("IPv6:") && strings.EqualFold(lit[:len("IPv6:")], "IPv6:") {
			return validateIPv6(lit[len("IPv6:"):])
		}

		return validateIPv4(lit)
	}

	// Email domains follow the RFC 5321 sub-domain grammar
	// (sub-domain = Let-dig [Ldh-str]), which permits an all-numeric top-level
	// label, so the numeric-TLD ban from the hostname format must not apply.
	// The grammar also has no trailing-dot production, so the FQDN root-dot
	// allowance from the hostname format must not apply either.
	return validateHostnameLabels(d, false, false)
}

func validateHostname(s string) error {
	// The hostname format is RFC 1123-based; the top-level label must not be
	// all-numeric, to disambiguate from an IPv4 address (RFC 1123 §2.1), and
	// the DNS root-dot convention permits a trailing dot on a multi-label FQDN.
	return validateHostnameLabels(s, true, true)
}

// validateHostnameLabels validates the shared RFC 1123 label structure used by
// both the hostname format and email domain validation. The banNumericTLD flag
// rejects an all-numeric top-level label, which the hostname format requires
// (RFC 1123 §2.1) but the RFC 5321 email domain grammar permits. The
// allowTrailingDot flag accepts the DNS root-dot convention on a multi-label
// FQDN, which likewise belongs to the hostname format only: the RFC 5321
// Domain grammar (sub-domain *("." sub-domain)) has no trailing-dot
// production.
func validateHostnameLabels(s string, banNumericTLD, allowTrailingDot bool) error {
	if s == "" {
		return errInvalidHostname
	}

	// Allow a single trailing dot on a multi-label FQDN (e.g. "example.com."),
	// but reject a bare trailing dot like "example." or ".".
	if allowTrailingDot && strings.HasSuffix(s, ".") {
		trimmed := s[:len(s)-1]
		if !strings.Contains(trimmed, ".") {
			return errInvalidHostname
		}

		s = trimmed
	}

	// Measure the 253-octet limit after trimming the trailing dot, which RFC
	// 1035/1123 do not count toward it.
	if len(s) > 253 {
		return errInvalidHostname
	}

	labels := strings.Split(s, ".")
	for _, label := range labels {
		if !isLDHLabel(label) {
			return errInvalidHostname
		}

		// Only labels carrying the "xn--" ACE prefix are A-labels and must
		// decode as valid Punycode (RFC 5890 §2.3.2.1). The prefix match is
		// case-insensitive per RFC 5890 §2.3.2.1. A plain RFC 1123 label may
		// contain interior hyphens, including consecutive ones at positions 3-4
		// (e.g. "ab--cd"), so the IDNA check must not apply to it; the hostname
		// format is RFC 1123-based and permits such labels.
		if hasACEPrefix(label) {
			u, err := idna.Lookup.ToUnicode(label)
			if err != nil {
				return errInvalidHostname
			}

			err = checkContextualRules(u)
			if err != nil {
				return err
			}
		}
	}

	if banNumericTLD && isAllDigits(labels[len(labels)-1]) {
		return errors.New("invalid hostname: numeric top-level label")
	}

	return nil
}

// isLDHLabel reports whether label is a letter-digit-hyphen label: ASCII
// letters, digits, and interior hyphens, non-empty and at most 63 octets. This
// is both the RFC 1123 §2.1 host name label and the RFC 5321 §4.1.2 sub-domain
// (Let-dig [Ldh-str]), which are the same production. Consecutive hyphens are
// permitted anywhere they are interior, including positions 3-4: RFC 5890
// §2.3.2.2 reserves that shape for IDNA, but neither RFC 1123 nor RFC 5321 does,
// and the two callers that own those grammars must not inherit the IDNA rule.
func isLDHLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}

	for i := range len(label) {
		c := label[i]

		isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		isHyphen := c == '-' && i > 0 && i < len(label)-1

		if !isAlpha && !isDigit && !isHyphen {
			return false
		}
	}

	return true
}

// hasACEPrefix reports whether label begins with the IDNA ACE prefix "xn--".
// The match is case-insensitive, since RFC 5890 §2.3.2.1 defines the prefix
// without regard to case.
func hasACEPrefix(label string) bool {
	if len(label) < 4 {
		return false
	}

	return (label[0] == 'x' || label[0] == 'X') &&
		(label[1] == 'n' || label[1] == 'N') &&
		label[2] == '-' && label[3] == '-'
}

// isAllDigits reports whether s is non-empty and contains only ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

// validateURIAbs validates an absolute URI/IRI: it must parse, carry a scheme,
// contain no forbidden characters, and bracket any bare IPv6 host. The badChars
// predicate and label select the URI-vs-IRI character rule and error wording.
func validateURIAbs(s string, badChars func(string) bool, label string) error {
	u, err := url.Parse(s)
	if err != nil {
		u = reparseUnparsedHost(s)
		if u == nil {
			return fmt.Errorf("invalid %s", label)
		}
	}

	if u.Scheme == "" {
		return fmt.Errorf("invalid %s: missing scheme", label)
	}

	if badChars(s) {
		return fmt.Errorf("invalid %s: forbidden characters", label)
	}

	if !validPctEncoding(s) {
		return fmt.Errorf("invalid %s: malformed percent-encoding", label)
	}

	if !validURIDelims(s, u.Host) || !validAuthorityDelims(s) {
		return fmt.Errorf("invalid %s: misplaced delimiter", label)
	}

	if !validIPLiteral(s) {
		return fmt.Errorf("invalid %s: malformed IP-literal", label)
	}

	// Bare IPv6 addresses must be enclosed in brackets per RFC 3986 §3.2.2.
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fmt.Errorf("invalid %s: bare IPv6 address", label)
	}

	return nil
}

// validateURIRef validates a URI/IRI reference: it must parse and contain no
// forbidden characters, but needs no scheme (relative references are allowed).
func validateURIRef(s string, badChars func(string) bool, label string) error {
	u, err := url.Parse(s)
	if err != nil {
		u = reparseUnparsedHost(s)
		if u == nil {
			return fmt.Errorf("invalid %s reference", label)
		}
	}

	if badChars(s) {
		return fmt.Errorf("invalid %s reference: forbidden characters", label)
	}

	if !validPctEncoding(s) {
		return fmt.Errorf("invalid %s reference: malformed percent-encoding", label)
	}

	if !validURIDelims(s, u.Host) || !validAuthorityDelims(s) {
		return fmt.Errorf("invalid %s reference: misplaced delimiter", label)
	}

	if !validIPLiteral(s) {
		return fmt.Errorf("invalid %s reference: malformed IP-literal", label)
	}

	// Bare IPv6 addresses must be enclosed in brackets per RFC 3986 §3.2.2; a
	// network-path reference carries the same authority grammar as an absolute
	// URI.
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return fmt.Errorf("invalid %s reference: bare IPv6 address", label)
	}

	return nil
}

// validURIDelims reports whether s uses the RFC 3986 gen-delims '#', '[', and
// ']' only in their delimiting positions: '#' at most once (introducing the
// fragment), and square brackets only as the enclosing pair of an authority
// IP-literal host. The net/url parser tolerates all three elsewhere (storing
// "a#b" as a fragment and accepting raw brackets in the path), but fragment =
// query =
// *( pchar / "/" / "?" ) contains no '#', and '[' / ']' appear in the RFC 3986
// grammar only in the IP-literal production. Counting on the raw string is
// deliberate: percent-encoded %5B/%5D are legal anywhere and leave no literal
// bracket in s. The host is the parsed authority host, which net/url returns
// bracketed exactly when the authority carries an IP-literal.
func validURIDelims(s, host string) bool {
	if strings.Count(s, "#") > 1 {
		return false
	}

	want := 0
	if strings.HasPrefix(host, "[") {
		want = 1
	}

	return strings.Count(s, "[") == want && strings.Count(s, "]") == want
}

// validIPLiteral reports whether the bracketed host in the authority of s,
// when there is one, is an RFC 3986 section 3.2.2 IP-literal as written:
// IP-literal = "[" ( IPv6address / IPvFuture ) "]". An IPvFuture literal is
// accepted here because reparseIPvFutureHost has already matched its
// production, and an IPv6address is what [url.Parse] has already run through
// netip.ParseAddr, with one gap: net/url implements the RFC 6874 zone
// identifier ("[fe80::1%25en0]"), which the RFC 3986 IPv6address production
// does not have. A '%' inside the brackets is therefore the one thing left to
// refuse.
func validIPLiteral(s string) bool {
	start, end, ok := rawAuthoritySpan(s)
	if !ok {
		return true
	}

	authority := s[start:end]

	open := strings.IndexByte(authority, '[')
	if open < 0 {
		return true
	}

	stop := strings.IndexByte(authority[open:], ']')
	if stop < 0 {
		return true // validURIDelims refuses the unpaired bracket
	}

	lit := authority[open+1 : open+stop]
	if lit != "" && (lit[0] == 'v' || lit[0] == 'V') {
		return true
	}

	return !strings.Contains(lit, "%")
}

// validPctEncoding reports whether every '%' in s begins a well-formed
// pct-encoded triplet ("%" HEXDIG HEXDIG). RFC 3986 uses the one production
// in every component, and net/url checks it everywhere but the query, which
// it stores raw.
func validPctEncoding(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}

		if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
			return false
		}

		i += 2
	}

	return true
}

// validAuthorityDelims reports whether the authority component of s, as
// written, carries at most one '@'. RFC 3986 section 3.2.1 excludes '@' from
// userinfo, so a second one belongs to no component; net/url splits at the
// last '@' and admits the first into userinfo.
func validAuthorityDelims(s string) bool {
	start, end, ok := rawAuthoritySpan(s)

	return !ok || strings.Count(s[start:end], "@") <= 1
}

// rawAuthoritySpan returns the byte range of the authority component of s as
// written, and whether s carries one: the text after the scheme's "//" (or
// after a leading "//" for a network-path reference) up to the first "/",
// "?", or "#".
func rawAuthoritySpan(s string) (int, int, bool) {
	start := 0

	if i := strings.IndexByte(s, ':'); i > 0 && isSchemeName(s[:i]) {
		start = i + 1
	}

	if !strings.HasPrefix(s[start:], "//") {
		return 0, 0, false
	}

	start += 2

	end := len(s)
	if i := strings.IndexAny(s[start:], "/?#"); i >= 0 {
		end = start + i
	}

	return start, end, true
}

// isSchemeName reports whether s matches the RFC 3986 scheme production:
// ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ).
func isSchemeName(s string) bool {
	if s == "" || !isASCIILetter(s[0]) {
		return false
	}

	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isASCIILetter(c) && (c < '0' || c > '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}

	return true
}

// isASCIILetter reports whether c is an ASCII letter.
func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// reparseUnparsedHost retries a URL net/url refused for an authority it
// cannot represent but the RFCs admit: an IPvFuture literal, a reg-name
// carrying a percent-encoded ASCII octet (net/url unescapes a host and permits
// only escapes at or above 0x80), or a userinfo carrying a code point above
// ASCII (net/url reads userinfo as ASCII, where RFC 3987 iuserinfo admits
// ucschar). The returned URL serves the remaining structural checks, which
// read the original string wherever they are character-based, so the URI
// validators still refuse the non-ASCII userinfo through their character
// rule.
func reparseUnparsedHost(s string) *url.URL {
	if u := reparseIPvFutureHost(s); u != nil {
		return u
	}

	return reparseAuthority(s)
}

// reparseAuthority re-parses s with every well-formed pct-encoded ASCII octet
// in its authority, and every code point above ASCII in its userinfo,
// replaced by a letter, so a reg-name such as "ex%41mple.com" (legal per RFC
// 3986 section 3.2.2, merely non-normalized) and a userinfo such as
// "é@host" (legal per RFC 3987 section 2.2) parse. A malformed triplet is
// left in place, so it still fails. A bracketed IP-literal is copied as
// written, because RFC 3986 section 3.2.2 admits no pct-encoded octet inside
// one (IP-literal = "[" ( IPv6address / IPvFuture ) "]"), so "[::1%41]" must
// keep failing rather than parse as "[::1a]". It returns nil when the
// authority holds nothing to rewrite or the rewrite fails to parse.
func reparseAuthority(s string) *url.URL {
	start, end, ok := rawAuthoritySpan(s)
	if !ok {
		return nil
	}

	// The userinfo ends at the first "@" of the authority; a second one is
	// refused later by validAuthorityDelims on the original string.
	userinfoEnd := start
	if at := strings.IndexByte(s[start:end], '@'); at >= 0 {
		userinfoEnd = start + at
	}

	var (
		b       strings.Builder
		changed bool
	)

	for i := start; i < end; i++ {
		if s[i] == '[' {
			// The IP-literal runs to its closing bracket, or to the end of
			// the authority when it has none; either way it is left alone.
			stop := end
			if j := strings.IndexByte(s[i:end], ']'); j >= 0 {
				stop = i + j + 1
			}

			b.WriteString(s[i:stop])

			i = stop - 1

			continue
		}

		if s[i] == '%' && i+2 < end && isHexDigit(s[i+1]) && isHexDigit(s[i+2]) {
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err == nil && v < 0x80 {
				b.WriteByte('a')

				i += 2
				changed = true

				continue
			}
		}

		if i < userinfoEnd && s[i] >= 0x80 {
			_, size := utf8.DecodeRuneInString(s[i:userinfoEnd])

			b.WriteByte('a')

			i += size - 1
			changed = true

			continue
		}

		b.WriteByte(s[i])
	}

	if !changed {
		return nil
	}

	u, err := url.Parse(s[:start] + b.String() + s[end:])
	if err != nil {
		return nil
	}

	return u
}

// reparseIPvFutureHost works around net/url's missing support for the RFC
// 3986 IPvFuture host literal (IP-literal = "[" ( IPv6address / IPvFuture )
// "]"): [url.Parse] validates a bracketed host strictly as an IP address, so a
// syntactically valid "[v1.x]" authority fails to parse. When s contains a
// bracketed segment matching the IPvFuture production, the URL is re-parsed
// with that literal swapped for a placeholder IPv6 literal and the result
// returned for the remaining checks (which still run on the original string
// where they are character-based: every IPvFuture character is a legal URI
// character, and the placeholder keeps the host bracketed so the IP-literal
// bracket accounting is unchanged). It returns nil when no such literal is
// present or the re-parse fails too.
func reparseIPvFutureHost(s string) *url.URL {
	start := strings.IndexByte(s, '[')
	if start < 0 {
		return nil
	}

	end := strings.IndexByte(s[start:], ']')
	if end < 0 {
		return nil
	}

	end += start

	if !isIPvFuture(s[start+1 : end]) {
		return nil
	}

	u, err := url.Parse(s[:start] + "[::1]" + s[end+1:])
	if err != nil {
		return nil
	}

	return u
}

// isIPvFuture reports whether lit (the text between the brackets of an
// IP-literal) matches the RFC 3986 IPvFuture production: "v" 1*HEXDIG "."
// 1*( unreserved / sub-delims / ":" ). The "v" is case-insensitive, as ABNF
// string literals are (RFC 5234 §2.3).
func isIPvFuture(lit string) bool {
	if lit == "" || (lit[0] != 'v' && lit[0] != 'V') {
		return false
	}

	i := 1
	for i < len(lit) && isHexDigit(lit[i]) {
		i++
	}

	if i == 1 || i >= len(lit) || lit[i] != '.' {
		return false
	}

	tail := lit[i+1:]
	if tail == "" {
		return false
	}

	for j := range len(tail) {
		if !isIPvFutureTailChar(tail[j]) {
			return false
		}
	}

	return true
}

// isIPvFutureTailChar reports whether c may appear after the dot in an
// IPvFuture literal: unreserved / sub-delims / ":".
func isIPvFutureTailChar(c byte) bool {
	if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
		return true
	}

	switch c {
	case '-', '.', '_', '~', // unreserved
		'!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', // sub-delims
		':':
		return true
	}

	return false
}

// validateURI validates an absolute URI per RFC 3986.
func validateURI(s string) error {
	return validateURIAbs(s, containsInvalidURIChars, "URI")
}

// validateURIReference validates a URI-reference per RFC 3986 (relative allowed).
func validateURIReference(s string) error {
	return validateURIRef(s, containsInvalidURIChars, "URI")
}

// containsForbiddenURIIRIChars scans s for characters forbidden by RFC 3986
// (URI) and RFC 3987 (IRI). When asciiOnly is true it additionally rejects any
// code point above '~' (0x7E), the one rule URIs add over IRIs.
func containsForbiddenURIIRIChars(s string, asciiOnly bool) bool {
	for _, c := range s {
		if (asciiOnly && c > 0x7E) || isForbiddenURIIRIChar(c) {
			return true
		}
	}

	return false
}

// containsInvalidURIChars checks for characters forbidden by RFC 3986. A URI is
// limited to ASCII, so any code point above '~' (0x7E) is also rejected.
func containsInvalidURIChars(s string) bool {
	return containsForbiddenURIIRIChars(s, true)
}

// isForbiddenURIIRIChar reports whether c is one of the gen-delims/sub-delims
// and other characters that both RFC 3986 (URI) and RFC 3987 (IRI) exclude from
// the unreserved/reserved sets. It covers only the rules the two share; their
// genuinely different rule (URIs additionally ban all non-ASCII) lives in
// containsInvalidURIChars.
func isForbiddenURIIRIChar(c rune) bool {
	// Control code points are excluded by both RFC 3986 and RFC 3987 (RFC 3987
	// ucschar begins at U+00A0). The net/url byte check catches C0 and DEL, but
	// a raw C1 control (U+0080-U+009F) encodes as two bytes >= 0x20 and slips
	// past it on the IRI path, where asciiOnly is false.
	if c <= 0x1F || c == 0x7F || (c >= 0x80 && c <= 0x9F) {
		return true
	}

	switch c {
	case ' ', '<', '>', '{', '}', '^', '`', '|', '\\', '"':
		return true
	}

	return false
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func validateUUID(s string) error {
	if len(s) != 36 {
		return errInvalidUUID
	}

	for i := range 36 {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return errInvalidUUID
			}

		default:
			if !isHexDigit(s[i]) {
				return errInvalidUUID
			}
		}
	}

	return nil
}

func validateIPv4(s string) error {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return errInvalidIPv4
	}

	// Ensure it's actually written in dotted-decimal, not ::ffff:a.b.c.d.
	if strings.Contains(s, ":") {
		return errInvalidIPv4
	}

	return nil
}

func validateIPv6(s string) error {
	ip := net.ParseIP(s)
	if ip == nil {
		return errInvalidIPv6
	}

	// Must contain a colon to be IPv6.
	if !strings.Contains(s, ":") {
		return errInvalidIPv6
	}

	return nil
}

func validateJSONPointer(s string) error {
	if s == "" {
		return nil // empty string is a valid JSON Pointer (root)
	}

	if !strings.HasPrefix(s, "/") {
		return errors.New("invalid JSON Pointer: must start with /")
	}

	// Check for invalid escape sequences.
	for i := range len(s) {
		if s[i] == '~' {
			if i+1 >= len(s) || (s[i+1] != '0' && s[i+1] != '1') {
				return errors.New("invalid JSON Pointer: invalid escape sequence")
			}
		}
	}

	return nil
}

// regexState is what the regex scan has just read, which decides whether a
// quantifier may follow.
type regexState int

const (
	// Nothing to repeat: the pattern start, the byte after '(' or '|', and
	// the byte after a lazy suffix.
	regexNothing regexState = iota
	// An atom a quantifier may follow.
	regexAtom
	// A quantifier, which a single lazy '?' may follow.
	regexQuantifier
)

// validateRegex checks that s is a valid ECMA 262 regular expression. The
// "regex" format is defined in terms of ECMA 262, which is a superset of Go's
// RE2 (it permits backreferences and lookaround). A structural check is used
// rather than [regexp.Compile] so valid ECMA 262 patterns that RE2 rejects are
// still accepted, while genuinely malformed patterns are rejected.
//
// Beyond balanced groups, terminated classes, and well-formed escapes, the
// scan holds every quantifier to something to repeat: ECMA 262 22.2.1 derives
// Term from Atom Quantifier, so "*a", "a**", and "{1}" are syntax errors in
// every engine, and Annex B keeps the early error that the first bound of
// "{m,n}" must not exceed the second. A '{' that opens no braced quantifier
// form is an Annex B ExtendedPatternCharacter, so "a{,5}" and "a{2,1" stay
// literal. The assertions '^' and '$' and the word-boundary escapes "\b" and
// "\B" are left quantifiable, as RE2 reads them, and so is a lookahead, which
// Annex B's QuantifiableAssertion covers; a lookbehind is not, since no Term
// production lets a Quantifier follow it.
// A class range whose two ends are literal code points must run upward, the
// early error 22.2.1.1 keeps; a range with an escape on either side is left
// alone, since Annex B lets a class escape stand beside a literal '-'. A
// capture name defined twice is refused where both groups can take part in
// one match, the early error 22.2.1.1 keeps, and admitted across the
// alternatives of one disjunction, as ES2025 reads it.
func validateRegex(s string) error {
	// ECMA 262 reads a pattern as code points, so a byte sequence that is
	// not UTF-8 holds no source character; every engine refuses it. The
	// scan below reads bytes, and only this check keeps a stray byte from
	// passing as an atom or a class member.
	if !utf8.ValidString(s) {
		return errors.New("invalid regex: invalid UTF-8")
	}

	// The open groups, with the pattern itself at the bottom so its
	// alternatives scope capture names the way a group's do.
	groups := []regexGroup{{}}

	inClass := false

	// The last literal class atom (-1 for none) and whether a '-' has
	// opened a range from it, so the closing atom can be checked against
	// it.
	classPrev := rune(-1)
	classDash := false

	state := regexNothing

	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 >= len(s) {
				return errors.New("invalid regex: trailing backslash")
			}

			r, size := utf8.DecodeRuneInString(s[i+1:])

			err := validateRegexEscape(r, size)
			if err != nil {
				return err
			}

			i += 1 + size
			state = regexAtom

			// An escape's value is not read, so a range it bounds goes
			// unchecked.
			classPrev, classDash = -1, false

			continue

		case inClass:
			// The first unescaped ']' terminates the class. ECMA 262 22.2.1
			// permits an empty ClassContents, so "[]" (matches nothing) and
			// "[^]" (matches any character) are valid, and an unescaped ']' is
			// never a class member (ClassAtomNoDash excludes it; a literal ']'
			// must be escaped). "[]]" is therefore an empty class followed by a
			// literal ']', which Annex B permits outside a class.
			if c == ']' {
				inClass = false
				state = regexAtom

				break
			}

			r, size := utf8.DecodeRuneInString(s[i:])

			switch {
			case c == '-' && classPrev >= 0 && !classDash && i+1 < len(s) && s[i+1] != ']':
				classDash = true
			case classDash:
				if r < classPrev {
					return errors.New("invalid regex: class range out of order")
				}

				classPrev, classDash = -1, false

			default:
				classPrev = r
			}

			i += size

			continue

		case c == '[':
			inClass = true
			classPrev, classDash = -1, false

			// A leading '^' negates the class and is no class atom, so it
			// bounds no range.
			if i+1 < len(s) && s[i+1] == '^' {
				i++
			}

		case c == '(':
			state = regexNothing
			parent := &groups[len(groups)-1]
			lookbehind := false

			// A "(?" opens a non-capturing, lookaround, named, or (in RE2)
			// flagged group; its modifier is consumed here so no byte of it
			// counts as an atom for a quantifier to repeat.
			if i+1 < len(s) && s[i+1] == '?' {
				mod := s[i+2:]

				// A "(?" that opens none of the known modifiers is a
				// malformed group: an empty or ill-formed capture name, or
				// nothing at all. Reading its bytes as atoms would accept it.
				n, name := regexGroupModifierLen(mod)
				if n == 0 {
					return errors.New("invalid regex: malformed group modifier")
				}

				// A capture name joins the alternative that holds the group,
				// so the group's own body sees it too.
				if name != "" {
					if slices.Contains(parent.names, name) {
						return errors.New("invalid regex: duplicate capture group name")
					}

					parent.names = append(parent.names, name)
				}

				lookbehind = len(mod) > 1 && mod[0] == '<' && (mod[1] == '=' || mod[1] == '!')
				i += 1 + n
			}

			groups = append(groups, regexGroup{
				lookbehind: lookbehind,
				base:       slices.Clip(parent.names),
				names:      slices.Clip(parent.names),
			})

		case c == ')':
			if len(groups) == 1 {
				return errRegexUnbalancedParenthesis
			}

			group := groups[len(groups)-1]
			groups = groups[:len(groups)-1]

			// Every name the group defined, in any alternative, can take
			// part in a match beside a name defined after it.
			parent := &groups[len(groups)-1]
			parent.names = append(parent.names, group.closed...)
			parent.names = append(parent.names, group.names[len(group.base):]...)

			state = regexAtom
			if group.lookbehind {
				state = regexNothing
			}

		case c == '|':
			state = regexNothing

			// The names of the alternative just closed cannot take part in
			// a match beside those of the next one, so the next one starts
			// from the names visible when the group opened.
			group := &groups[len(groups)-1]
			group.closed = append(group.closed, group.names[len(group.base):]...)
			group.names = group.base

		case c == '*', c == '+':
			if state != regexAtom {
				return errRegexNothingToRepeat
			}

			state = regexQuantifier

		case c == '?':
			switch state {
			case regexAtom:
				state = regexQuantifier
			case regexQuantifier:
				state = regexNothing // the lazy suffix
			case regexNothing:
				return errRegexNothingToRepeat
			}

		case c == '{':
			n, ordered := regexBracedQuantifier(s[i:])
			if n == 0 {
				state = regexAtom // an Annex B ExtendedPatternCharacter

				break
			}

			if state != regexAtom {
				return errRegexNothingToRepeat
			}

			if !ordered {
				return errors.New("invalid regex: quantifier bounds out of order")
			}

			state = regexQuantifier
			i += n

			continue

		default:
			state = regexAtom
		}

		i++
	}

	if len(groups) != 1 {
		return errRegexUnbalancedParenthesis
	}

	if inClass {
		return errors.New("invalid regex: unterminated character class")
	}

	return nil
}

// regexGroup is one open group on validateRegex's stack. The pattern itself
// sits at the bottom, so the top level scopes capture names the way a group
// does.
type regexGroup struct {
	// The capture names visible when the group opened (base), and those
	// plus the names defined since the group's last '|' (names). A name
	// already in names is a duplicate: ECMA 262 22.2.1.1 refuses two groups
	// of one name only where both can take part in a match, which two
	// alternatives of one disjunction never do. Both slices are clipped, so
	// an append copies rather than writing into the parent's storage.
	base, names []string

	// The names defined in the alternatives a '|' already closed, which the
	// group hands to its parent when it closes.
	closed []string

	// Whether the group is a lookbehind, so the closing parenthesis knows
	// whether a quantifier may follow it.
	lookbehind bool
}

// regexGroupModifierLen returns the length of the group modifier that follows
// "(?" at the start of s: the lookaround and non-capturing introducers ":",
// "=", "!", "<=", and "<!", a named-capture name "<name>" (RE2 spells it
// "P<name>"), or a flag run such as "i" or "ims-U:". It returns 0 when s
// opens none of them, which the scan reports as a malformed group. The
// second result is the decoded capture name for the named forms and empty
// for the rest.
func regexGroupModifierLen(s string) (int, string) {
	if s == "" {
		return 0, ""
	}

	switch {
	case s[0] == ':' || s[0] == '=' || s[0] == '!':
		return 1, ""
	case s[0] == '<' && len(s) > 1 && (s[1] == '=' || s[1] == '!'):
		return 2, ""
	case s[0] == '<':
		return regexGroupNameLen(s, 1)
	case s[0] == 'P' && len(s) > 1 && s[1] == '<':
		return regexGroupNameLen(s, 2)
	}

	return regexFlagRunLen(s), ""
}

// regexFlagRunLen returns the length of the flag run at the start of s, the
// form RE2's parsePerlFlags reads after "(?": letters from "imsU", at most
// one '-' with a letter on its far side, and a ':' (consumed) or ')' (left
// for the group accounting) closing the run. ECMA 262's modifiers "(?ims-ims:"
// are a subset of the same shape. A run holding any other byte, a bare '-',
// or no letter at all is no modifier, so the function returns 0 and the scan
// reports a malformed group.
func regexFlagRunLen(s string) int {
	sawFlag := false
	negated := false

	for n := range len(s) {
		switch s[n] {
		case 'i', 'm', 's', 'U':
			sawFlag = true
		case '-':
			if negated {
				return 0
			}

			negated = true
			sawFlag = false

		case ':':
			if !sawFlag {
				return 0
			}

			return n + 1

		case ')':
			if !sawFlag {
				return 0
			}

			return n

		default:
			return 0
		}
	}

	return 0
}

// regexGroupNameLen returns the length of s through the '>' closing a group
// name whose first character sits at s[from], with the name decoded to its
// code points, or 0 when the name is not an ECMA 262 RegExpIdentifierName
// ending in '>'. Each character of the name is a code point written literally
// or as a RegExpUnicodeEscapeSequence; the first must be an
// IdentifierStartChar and every later one an IdentifierPartChar, so a name
// never opens on a digit. Only such a run is consumed, so a malformed name
// never swallows a parenthesis the group accounting needs. The decoded form
// is what two spellings of one name share, so it is what the duplicate check
// compares.
func regexGroupNameLen(s string, from int) (int, string) {
	var name strings.Builder

	for i := from; i < len(s); {
		if s[i] == '>' {
			if i == from {
				return 0, ""
			}

			return i + 1, name.String()
		}

		r, size := regexGroupNameChar(s[i:])
		if size == 0 {
			return 0, ""
		}

		ok := isRegexIDPart(r)
		if i == from {
			ok = isRegexIDStart(r)
		}

		if !ok {
			return 0, ""
		}

		name.WriteRune(r)

		i += size
	}

	return 0, ""
}

// regexGroupNameChar reads one character of a group name at the start of s
// and returns its code point and the bytes it spans, or a size of 0 when s
// opens no character. A literal code point is one UTF-8 sequence. ECMA 262
// 22.2.1 also admits a RegExpUnicodeEscapeSequence in a name, read in Unicode
// mode whatever the pattern's flags: "\u" and four hex digits, a lead and a
// trail surrogate in that form joined to one code point, or "\u{" and hex
// digits up to 0x10FFFF "}". A lone surrogate keeps its own value, which the
// identifier tests refuse.
func regexGroupNameChar(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}

	if s[0] != '\\' {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			return 0, 0
		}

		return r, size
	}

	if len(s) < 2 || s[1] != 'u' {
		return 0, 0
	}

	if len(s) > 2 && s[2] == '{' {
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return 0, 0
		}

		r, ok := regexHexValue(s[3:end])
		if !ok || r > unicode.MaxRune {
			return 0, 0
		}

		return r, end + 1
	}

	const escapeLen = len(`\uXXXX`)

	if len(s) < escapeLen {
		return 0, 0
	}

	r, ok := regexHexValue(s[2:escapeLen])
	if !ok {
		return 0, 0
	}

	if !utf16.IsSurrogate(r) || r >= 0xDC00 || len(s) < 2*escapeLen ||
		s[escapeLen] != '\\' || s[escapeLen+1] != 'u' {
		return r, escapeLen
	}

	trail, ok := regexHexValue(s[escapeLen+2 : 2*escapeLen])
	if !ok || trail < 0xDC00 || trail > 0xDFFF {
		return r, escapeLen
	}

	return utf16.DecodeRune(r, trail), 2 * escapeLen
}

// regexHexValue reads s as a non-empty run of hex digits and returns its
// value, or false when s is empty, holds another byte, or exceeds the rune
// range. Leading zeros are admitted, as ECMA 262 HexDigits admits them.
func regexHexValue(s string) (rune, bool) {
	if s == "" {
		return 0, false
	}

	var v rune

	for i := range len(s) {
		var d rune

		switch c := s[i]; {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, false
		}

		v = v<<4 | d
		if v > unicode.MaxRune {
			return 0, false
		}
	}

	return v, true
}

// isRegexIDStart reports whether r is an ECMA 262 IdentifierStartChar: a
// code point with the Unicode ID_Start property, '$', or '_'.
func isRegexIDStart(r rune) bool {
	if r == '$' || r == '_' {
		return true
	}

	if unicode.In(r, unicode.Pattern_Syntax, unicode.Pattern_White_Space) {
		return false
	}

	return unicode.In(r, unicode.L, unicode.Nl, unicode.Other_ID_Start)
}

// isRegexIDPart reports whether r is an ECMA 262 IdentifierPartChar: a code
// point with the Unicode ID_Continue property, '$', ZWNJ, or ZWJ.
func isRegexIDPart(r rune) bool {
	if isRegexIDStart(r) || r == '\u200C' || r == '\u200D' {
		return true
	}

	if unicode.In(r, unicode.Pattern_Syntax, unicode.Pattern_White_Space) {
		return false
	}

	return unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc, unicode.Other_ID_Continue)
}

// regexBracedQuantifier reads a braced quantifier at the start of s, which
// begins with '{': the ECMA 262 QuantifierPrefix forms "{m}", "{m,}", and
// "{m,n}" with m and n runs of decimal digits. It returns the length of the
// form and whether its bounds are in order (m <= n, or a single bound), or 0
// when s opens none of the forms, in which case the '{' is a literal.
func regexBracedQuantifier(s string) (int, bool) {
	i := 1

	lowStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}

	if i == lowStart {
		return 0, false
	}

	low := s[lowStart:i]

	if i < len(s) && s[i] == '}' {
		return i + 1, true
	}

	if i >= len(s) || s[i] != ',' {
		return 0, false
	}

	i++

	highStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}

	if i >= len(s) || s[i] != '}' {
		return 0, false
	}

	if i == highStart {
		return i + 1, true
	}

	return i + 1, decimalLessOrEqual(low, s[highStart:i])
}

// decimalLessOrEqual reports whether the decimal digit string a is at most b,
// compared as unbounded integers so a bound too large for a machine word is
// still ordered correctly.
func decimalLessOrEqual(a, b string) bool {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")

	if len(a) != len(b) {
		return len(a) < len(b)
	}

	return a <= b
}

// validateRegexEscape reports whether c, the rune following a backslash, forms
// a valid escape in an ECMA 262 regular expression. ECMA 262 Annex B, which is
// normative for web browsers and shipped by every JS engine, widens
// IdentityEscape[~UnicodeMode] from the main grammar's "SourceCharacter but not
// UnicodeIDContinue" to "SourceCharacter but not c", so a source character in
// the escape position either names a defined escape such as \d, \n, \u, or \1,
// or is its own identity escape, and no character there rejects. Both "\a"
// and "\_" are identity escapes under Annex B even though the main grammar's
// narrower rule excludes them.
//
// Bare "\c" with no following ControlLetter is accepted because Annex B
// accepts it: ExtendedAtom carries a "\ [lookahead = c]" production, so the
// backslash before a non-ControlLetter "c" is its own literal atom.
//
// The one rejection is a lone invalid UTF-8 byte, which decodes to
// (utf8.RuneError, 1) and is not a source character at all; a genuine U+FFFD
// decodes from three bytes and is accepted as an identity escape. The size is
// the byte length the rune decoded from, which distinguishes the two.
func validateRegexEscape(c rune, size int) error {
	if c == utf8.RuneError && size == 1 {
		return errors.New("invalid regex: invalid escape sequence")
	}

	return nil
}

// validateRelativeJSONPointer validates a Relative JSON Pointer per
// draft-handrews-relative-json-pointer. The format is a non-negative integer
// followed by either a JSON Pointer or a '#'.
func validateRelativeJSONPointer(s string) error {
	if s == "" {
		return errors.New("invalid relative JSON Pointer: empty string")
	}

	// Parse leading non-negative integer (ASCII digits only).
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}

	if i == 0 {
		return errors.New("invalid relative JSON Pointer: must start with digit")
	}

	// No leading zeros on multi-digit numbers.
	if i > 1 && s[0] == '0' {
		return errors.New("invalid relative JSON Pointer: leading zero")
	}

	rest := s[i:]
	if rest == "" || rest == "#" {
		return nil
	}

	return validateJSONPointer(rest)
}

// validateDuration validates an ISO 8601 duration string per RFC 3339 Appendix A.
// Format: P[nY][nM][nW][nD][T[nH][nM][nS]]. Fractional seconds are not accepted;
// RFC 3339 Appendix A's grammar (dur-second = 1*DIGIT "S") permits none.
func validateDuration(s string) error {
	if s == "" || s[0] != 'P' {
		return errors.New("invalid duration: must start with P")
	}

	s = s[1:]
	if s == "" {
		return errDurationNoComponents
	}

	hasComponent := false
	inTime := false
	hasWeek := false
	dateComponents := 0
	lastDateOrder := -1
	lastTimeOrder := -1

	for s != "" {
		if s[0] == 'T' {
			if inTime {
				return errors.New("invalid duration: duplicate T")
			}

			inTime = true
			s = s[1:]
			if s == "" {
				return errors.New("invalid duration: T without time component")
			}

			continue
		}

		// Parse digits (ASCII only). RFC 3339 ABNF permits no fractional parts,
		// so any non-digit other than the designator (e.g. '.' or ',') falls
		// through to checkDurationOrder and is rejected as an unknown designator.
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}

		if i == 0 || i >= len(s) {
			return errors.New("invalid duration: missing designator")
		}

		designator := s[i]
		s = s[i+1:]

		switch {
		case inTime:
			next, err := checkDurationOrder(durationTimeOrder, designator, lastTimeOrder, "time")
			if err != nil {
				return err
			}

			lastTimeOrder = next

		case designator == 'W':
			// Weeks are a standalone alternative outside the Y/M/D chain; the
			// combination check below rejects mixing them with other units.
			hasWeek = true
			dateComponents++

		default:
			next, err := checkDurationOrder(durationDateOrder, designator, lastDateOrder, "date")
			if err != nil {
				return err
			}

			lastDateOrder = next
			dateComponents++
		}

		hasComponent = true
	}

	if !hasComponent {
		return errDurationNoComponents
	}

	// Weeks cannot be combined with other date or time components.
	if hasWeek && (dateComponents > 1 || inTime) {
		return errors.New("invalid duration: weeks cannot be combined with other units")
	}

	return nil
}

// checkDurationOrder validates designator against the ordered component map for
// the given kind ("date" or "time"). Components must form a contiguous chain in
// canonical order: the first component (last < 0) may be any designator, but
// each subsequent one must immediately follow the previous (no gaps, repeats,
// or reordering). This enforces the RFC 3339 ABNF nesting, where dur-year =
// nY [dur-month]. A sequence like P1Y2D (year then day, skipping month) is
// therefore rejected. It returns the designator's order index.
func checkDurationOrder(order map[byte]int, designator byte, last int, kind string) (int, error) {
	cur, ok := order[designator]
	if !ok {
		return 0, fmt.Errorf("invalid duration: invalid %s designator", kind)
	}

	if last >= 0 && cur != last+1 {
		return 0, fmt.Errorf("invalid duration: %s components out of order", kind)
	}

	return cur, nil
}

// validateIRI validates an absolute IRI per RFC 3987. IRIs allow non-ASCII
// Unicode characters but otherwise follow URI structure (must have a scheme).
func validateIRI(s string) error {
	return validateURIAbs(s, containsInvalidIRIChars, "IRI")
}

// validateIRIReference validates an IRI-reference per RFC 3987 (relative allowed).
func validateIRIReference(s string) error {
	return validateURIRef(s, containsInvalidIRIChars, "IRI")
}

// containsInvalidIRIChars checks for characters forbidden by RFC 3987. Unlike
// URIs, IRIs allow non-ASCII Unicode characters, but only within the RFC 3987
// sets: iunreserved admits ucschar, and the private-use iprivate set is
// admitted only in the iquery component (neither ipath nor ifragment has an
// iprivate production). Every other non-ASCII code point -- the noncharacter
// gaps (U+FDD0-FDEF, U+FFF0-FFFD plus the plane-final U+FFFE/U+FFFF pairs)
// and the U+E0000-E0FFF tags-plane gap -- is rejected anywhere. Ill-formed
// UTF-8 decodes to U+FFFD, which the noncharacter-adjacent bounds already
// exclude, and range decoding never yields surrogates.
func containsInvalidIRIChars(s string) bool {
	// Locate the query component: from just after the first '?' preceding any
	// '#' up to that '#' (or the end of the string). A '?' inside the fragment
	// does not open a query.
	end := len(s)
	if frag := strings.IndexByte(s, '#'); frag >= 0 {
		end = frag
	}

	qStart := -1
	if q := strings.IndexByte(s[:end], '?'); q >= 0 {
		qStart = q + 1
	}

	for i, c := range s {
		if isForbiddenURIIRIChar(c) {
			return true
		}

		if c < 0xA0 {
			continue
		}

		inQuery := qStart >= 0 && i >= qStart && i < end
		if !isUcschar(c) && (!inQuery || !isIprivate(c)) {
			return true
		}
	}

	return false
}

// validateURITemplate validates a URI Template per RFC 6570. It checks for
// matched, non-nested braces, that each brace expression is non-empty and
// contains only valid expression characters, and that the literal text
// between expressions contains only characters from the literals rule
// (which excludes CTL, SP, '"', "'", '%' outside pct-encoded, '<', '>',
// '\', '^', '`', and '|', and limits non-ASCII to ucschar / iprivate).
func validateURITemplate(s string) error {
	inExpr := false

	exprStart := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '{':
			if inExpr {
				return errors.New("invalid URI template: nested brace")
			}

			inExpr = true
			exprStart = i + 1
			i++

		case c == '}':
			if !inExpr {
				return errors.New("invalid URI template: unmatched closing brace")
			}

			err := validateURITemplateExpr(s[exprStart:i])
			if err != nil {
				return err
			}

			inExpr = false
			i++

		case inExpr:
			// Expression contents are validated as a whole at the closing brace.
			i++

		case c == '%':
			// In literal context '%' is legal only as pct-encoded ("%" HEXDIG
			// HEXDIG).
			if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
				return errors.New("invalid URI template: malformed percent-encoding in literal")
			}

			i += 3

		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if !isURITemplateLiteral(r, size) {
				return errors.New("invalid URI template: invalid character in literal")
			}

			i += size
		}
	}

	if inExpr {
		return errors.New("invalid URI template: unmatched opening brace")
	}

	return nil
}

// isURITemplateLiteral reports whether r may appear as literal text outside a
// brace expression, per the RFC 6570 literals rule as corrected by errata ID
// 6937 (Verified): %x21 / %x23-24 / %x26-3B / %x3D / %x3F-5B / %x5D / %x5F /
// %x61-7A / %x7E / ucschar / iprivate ('%' is handled separately as
// pct-encoded, and '{' / '}' delimit expressions). The published rule splits
// that third arm into %x26 / %x28-3B, omitting U+0027 APOSTROPHE; the erratum
// restores it, and the RFC's own §2.1 example '{var}' relies on it. The size is
// the byte length the rune decoded from: a lone invalid UTF-8 byte decodes to
// (utf8.RuneError, 1) and is rejected.
func isURITemplateLiteral(r rune, size int) bool {
	if r == utf8.RuneError && size == 1 {
		return false
	}

	switch {
	case r == 0x21,
		r >= 0x23 && r <= 0x24,
		r >= 0x26 && r <= 0x3B,
		r == 0x3D,
		r >= 0x3F && r <= 0x5B,
		r == 0x5D,
		r == 0x5F,
		r >= 0x61 && r <= 0x7A,
		r == 0x7E:
		return true
	}

	return isUcschar(r) || isIprivate(r)
}

// isUcschar reports whether r is in the RFC 3987 ucschar set: %xA0-D7FF /
// %xF900-FDCF / %xFDF0-FFEF / %x10000-1FFFD through %xD0000-DFFFD (each
// supplementary plane minus its trailing noncharacters) / %xE1000-EFFFD.
func isUcschar(r rune) bool {
	switch {
	case r >= 0xA0 && r <= 0xD7FF,
		r >= 0xF900 && r <= 0xFDCF,
		r >= 0xFDF0 && r <= 0xFFEF:
		return true

	case r >= 0x10000 && r <= 0xDFFFD:
		// Planes 1-13 each span %xN0000-NFFFD, excluding the plane-final
		// noncharacters NFFFE and NFFFF.
		return r&0xFFFF <= 0xFFFD

	case r >= 0xE1000 && r <= 0xEFFFD:
		return true
	}

	return false
}

// isIprivate reports whether r is in the RFC 3987 iprivate set of private-use
// code points: %xE000-F8FF / %xF0000-FFFFD / %x100000-10FFFD.
func isIprivate(r rune) bool {
	return (r >= 0xE000 && r <= 0xF8FF) ||
		(r >= 0xF0000 && r <= 0xFFFFD) ||
		(r >= 0x100000 && r <= 0x10FFFD)
}

// validateURITemplateExpr validates the contents of a single {expression}
// against the RFC 6570 grammar (the text between the braces, with the braces
// already stripped):
//
//	expression    = [ operator ] variable-list
//	operator      = op-level2 / op-level3 / op-reserve
//	variable-list = varspec *( "," varspec )
//	varspec       = varname [ modifier-level4 ]
//	varname       = varchar *( ["."] varchar )
//	varchar       = ALPHA / DIGIT / "_" / pct-encoded
//
// The op-reserve operators ("=", ",", "!", "@", "|") are reserved by RFC 6570
// for future extensions; they are accepted as grammatically valid here.
func validateURITemplateExpr(e string) error {
	if e == "" {
		return errors.New("invalid URI template: empty expression")
	}

	// Strip a leading operator. Op-level2/op-level3 are "+#./;?&" and op-reserve
	// is "=,!@|"; both are a single character introducing the variable list.
	switch e[0] {
	case '+', '#', '.', '/', ';', '?', '&', // op-level2 / op-level3
		'=', ',', '!', '@', '|': // op-reserve (reserved, accepted)
		e = e[1:]
	}

	if e == "" {
		return errors.New("invalid URI template: operator without variable list")
	}

	// Variable-list = varspec *( "," varspec ); each varspec must be non-empty.
	for spec := range strings.SplitSeq(e, ",") {
		err := validateURITemplateVarspec(spec)
		if err != nil {
			return err
		}
	}

	return nil
}

// validateURITemplateVarspec validates a single RFC 6570 varspec:
// varname [ ":" max-length / "*" ], where max-length is 1-4 digits with a
// nonzero first digit.
func validateURITemplateVarspec(spec string) error {
	if spec == "" {
		return errors.New("invalid URI template: empty varspec")
	}

	// Split off a level-4 modifier: explode ("*") or prefix (":max-length").
	name := spec
	if before, after, ok := strings.Cut(spec, ":"); ok {
		name = before

		err := validateURITemplateMaxLength(after)
		if err != nil {
			return err
		}
	} else if star := strings.IndexByte(spec, '*'); star >= 0 {
		// The explode modifier must be the final character of the varspec.
		if star != len(spec)-1 {
			return errors.New("invalid URI template: characters after explode modifier")
		}

		name = spec[:star]
	}

	return validateURITemplateVarname(name)
}

// validateURITemplateMaxLength validates an RFC 6570 prefix max-length:
// 1-4 DIGITs whose first digit is nonzero.
func validateURITemplateMaxLength(s string) error {
	if s == "" {
		return errors.New("invalid URI template: empty max-length")
	}

	if len(s) > 4 {
		return errors.New("invalid URI template: max-length too long")
	}

	if s[0] == '0' {
		return errors.New("invalid URI template: max-length leading zero")
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return errors.New("invalid URI template: non-digit in max-length")
		}
	}

	return nil
}

// validateURITemplateVarname validates an RFC 6570 varname:
// varchar *( ["."] varchar ), where varchar = ALPHA / DIGIT / "_" /
// pct-encoded. A dot may separate varchars but may not lead, trail, or repeat.
func validateURITemplateVarname(name string) error {
	if name == "" {
		return errors.New("invalid URI template: empty varname")
	}

	if name[0] == '.' || name[len(name)-1] == '.' {
		return errors.New("invalid URI template: misplaced dot in varname")
	}

	if strings.Contains(name, "..") {
		return errors.New("invalid URI template: consecutive dots in varname")
	}

	for i := 0; i < len(name); {
		c := name[i]
		switch {
		case c == '.':
			i++
		case c == '%':
			// Pct-encoded = "%" HEXDIG HEXDIG.
			if i+2 >= len(name) || !isHexDigit(name[i+1]) || !isHexDigit(name[i+2]) {
				return errors.New("invalid URI template: malformed percent-encoding in varname")
			}

			i += 3

		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '_':
			i++
		default:
			return errors.New("invalid URI template: invalid character in varname")
		}
	}

	return nil
}

// validateIDNHostname validates an internationalized hostname per RFC 5890/5891.
func validateIDNHostname(s string) error {
	// The idn-hostname format bans an all-numeric top-level label, mirroring the
	// plain hostname format so it cannot be confused with an IPv4 address, and
	// keeps the FQDN trailing-dot allowance the hostname format has. Its ASCII
	// labels are IDNA LDH labels, so they go through idna.Lookup and pick up the
	// RFC 5890 §2.3.2.2 reserved-LDH ban.
	return validateIDNHostnameLabels(s, true, true, false)
}

// validateIDNHostnameLabels validates the shared RFC 5890/5891 label structure
// used by both the idn-hostname format and idn-email domain validation. Three
// flags separate what belongs to each caller's grammar:
//
// The banNumericTLD flag rejects an all-numeric top-level label, which the
// idn-hostname format requires but the RFC 5321/6531 email domain grammar
// permits. The allowTrailingDot flag accepts the DNS root-dot convention on a
// multi-label FQDN, which likewise belongs to the idn-hostname format only: the
// RFC 5321/6531 Domain grammar has no trailing-dot production.
//
// The asciiSubDomain flag decides which LDH rule an ASCII label without an ACE
// prefix is held to. Such a label is not internationalized at all, so the
// caller's grammar owns it: idn-hostname holds it to IDNA, where idna.Lookup
// applies RFC 5890 §2.3.2.2 and rejects a reserved-LDH label ("ab--cd"), while
// idn-email holds it to RFC 5321 sub-domain, which permits one -- RFC 6531 §3.3
// widens the RFC 5321 Domain grammar by admitting U-labels and does not narrow
// its ASCII alternative. This mirrors validateHostnameLabels, which likewise
// applies IDNA only to an "xn--"-prefixed label.
func validateIDNHostnameLabels(s string, banNumericTLD, allowTrailingDot, asciiSubDomain bool) error {
	if s == "" {
		return errors.New("invalid IDN hostname: empty string")
	}

	labels := splitIDNADots(s)

	// Allow a single trailing dot on a multi-label FQDN (e.g. "example.com."),
	// consistent with validateHostname. A bare trailing dot on a single label
	// ("example.") still leaves an empty label and is rejected below.
	if n := len(labels); allowTrailingDot && n >= 3 && labels[n-1] == "" {
		labels = labels[:n-1]
	}

	// The decoded (U-label) form of every label, kept for the RFC 5893 Bidi
	// rule, which reads across labels.
	decodedLabels := make([]string, 0, len(labels))

	totalLen := 0
	for i, label := range labels {
		if label == "" {
			return errors.New("invalid IDN hostname: empty label")
		}

		ascii, err := idnLabelToASCII(label, asciiSubDomain)
		if err != nil {
			return err
		}

		// A non-empty label whose A-label form is empty is a degenerate
		// A-label (e.g. "xn--" with an empty Punycode payload), which decodes
		// to an empty Unicode label and is malformed per RFC 5890.
		if ascii == "" {
			return errors.New("invalid IDN hostname: empty A-label")
		}

		// RFC 5890: A-labels must be at most 63 octets.
		if len(ascii) > 63 {
			return errors.New("invalid IDN hostname: label too long")
		}

		// RFC 5892 contextual rules apply to the U-label after the RFC 5891
		// section 5.3 mapping step, which idna.Lookup.ToUnicode performs, so
		// "L·L" is judged as "l·l" and a fullwidth letter as its ASCII
		// counterpart. An A-label takes the same call, because the ToASCII call
		// above leaves an already-ASCII A-label untouched and never re-checks
		// the contextual rules its U-label would fail. A pure ASCII label
		// without an ACE prefix stays as written: it may be a reserved-LDH
		// label the asciiSubDomain path admits and ToUnicode refuses, and ASCII
		// carries no CONTEXTO code point for the rules to judge.
		decoded := label
		if hasACEPrefix(label) || !isASCII(label) {
			decoded, err = idna.Lookup.ToUnicode(label)
			if err != nil {
				return fmt.Errorf("invalid IDN hostname: %w", err)
			}
		}

		err = checkContextualRules(decoded)
		if err != nil {
			return fmt.Errorf("invalid IDN hostname: %w", err)
		}

		decodedLabels = append(decodedLabels, decoded)

		totalLen += len(ascii)
		if i > 0 {
			totalLen++ // dot separator
		}
	}

	// RFC 5890: the full domain name in A-label form must not exceed 253 octets.
	if totalLen > 253 {
		return errors.New("invalid IDN hostname: name too long")
	}

	err := checkBidiRule(decodedLabels)
	if err != nil {
		return err
	}

	// When banNumericTLD is set, the top-level label must not be all-numeric,
	// mirroring validateHostname so that an idn-hostname cannot be confused with
	// an IPv4 address (RFC 1123 §2.1 / RFC 5890). The check uses the A-label
	// (IDNA-mapped) form, so a label of fullwidth digits (which IDNA-maps to
	// ASCII "123") is rejected too, not only a literal ASCII-digit label.
	// ToASCII already succeeded for every label in the loop.
	if banNumericTLD {
		tld := labels[len(labels)-1]

		ascii, err := idna.Lookup.ToASCII(tld)
		if err == nil && isAllDigits(ascii) {
			return errors.New("invalid IDN hostname: numeric top-level label")
		}
	}

	return nil
}

// checkBidiRule enforces the RFC 5893 Bidi rule across a whole domain name.
// Section 1.4 makes any name with an RTL label a Bidi domain name, and
// section 2 then holds every label in it, the LTR ones included, to the six
// conditions; in particular condition 1 refuses an LTR label that opens on a
// digit ("1host"). A label-by-label call to idna.Lookup applies the rule to
// one label at a time and never sees the RTL label next door, so the
// cross-label half is checked here on the decoded labels, the way idna does
// when handed the whole name.
func checkBidiRule(labels []string) error {
	isBidi := false

	for _, label := range labels {
		if bidirule.DirectionString(label) == bidi.RightToLeft {
			isBidi = true

			break
		}
	}

	if !isBidi {
		return nil
	}

	for _, label := range labels {
		if !bidirule.ValidString(label) {
			return errors.New("invalid IDN hostname: label violates the Bidi rule")
		}
	}

	return nil
}

// idnLabelToASCII returns the A-label form of one domain label. When
// asciiSubDomain is set and the label is pure ASCII without an ACE prefix, it is
// an LDH label the caller's grammar owns, so it is held to RFC 5321 sub-domain
// and returned as written rather than routed through idna.Lookup, whose RFC 5890
// §2.3.2.2 reserved-LDH ban does not belong to that grammar. Every other label
// -- one carrying non-ASCII (a U-label) or an "xn--" prefix (an A-label) -- is
// genuinely internationalized and converts through IDNA.
func idnLabelToASCII(label string, asciiSubDomain bool) (string, error) {
	if asciiSubDomain && isASCII(label) && !hasACEPrefix(label) {
		if !isLDHLabel(label) {
			return "", errors.New("invalid IDN hostname: invalid label")
		}

		return label, nil
	}

	ascii, err := idna.Lookup.ToASCII(label)
	if err != nil {
		return "", fmt.Errorf("invalid IDN hostname: %w", err)
	}

	return ascii, nil
}

// isASCII reports whether s consists entirely of ASCII bytes.
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}

	return true
}

// splitIDNADots splits a string on all IDNA dot separators:
// U+002E (full stop), U+3002 (ideographic full stop),
// U+FF0E (fullwidth full stop), U+FF61 (halfwidth ideographic full stop).
func splitIDNADots(s string) []string {
	var labels []string

	start := 0
	for i, r := range s {
		if r == '.' || r == '\u3002' || r == '\uFF0E' || r == '\uFF61' {
			labels = append(labels, s[start:i])
			start = i + utf8.RuneLen(r)
		}
	}

	labels = append(labels, s[start:])

	return labels
}

// checkContextualRules enforces RFC 5892 Appendix A contextual rules and
// rejects DISALLOWED exception characters that golang.org/x/net/idna does
// not check.
func checkContextualRules(label string) error {
	runes := []rune(label)

	// RFC 5891 section 5.4 requires every code point of a U-label to be
	// PVALID or CONTEXT under RFC 5892. The idna package answers UTS 46
	// lookup, which admits every code point UTS 46 marks NV8 (valid there,
	// not under IDNA2008), so the category gate runs here.
	for _, r := range runes {
		if !isIDNA2008Permitted(r) {
			return errors.New("invalid hostname: disallowed character")
		}
	}

	// Track whether the label contains any Hiragana, Katakana, or Han
	// characters (needed for KATAKANA MIDDLE DOT rule).
	hasCJK := false
	for _, r := range runes {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han) {
			hasCJK = true
			break
		}
	}

	for i, r := range runes {
		switch {
		// DISALLOWED exception characters.
		case r == '\u0640', // ARABIC TATWEEL
			r == '\u07FA',                  // NKO LAJANYALAN
			r == '\u302E',                  // HANGUL SINGLE DOT TONE MARK
			r == '\u302F',                  // HANGUL DOUBLE DOT TONE MARK
			r >= '\u3031' && r <= '\u3035', // CJK vertical kana repeat marks
			r == '\u303B':                  // VERTICAL IDEOGRAPHIC ITERATION MARK
			return errors.New("invalid hostname: disallowed character")

		// U+00B7 MIDDLE DOT: must be preceded AND followed by U+006C ('l').
		case r == '\u00B7':
			if i == 0 || i == len(runes)-1 || runes[i-1] != 'l' || runes[i+1] != 'l' {
				return errors.New("invalid hostname: MIDDLE DOT not between two 'l' characters")
			}

		// U+0375 GREEK KERAIA: must be followed by a Greek character.
		case r == '\u0375':
			if i == len(runes)-1 || !unicode.Is(unicode.Greek, runes[i+1]) {
				return errors.New("invalid hostname: GREEK KERAIA not followed by Greek character")
			}

		// U+05F3 HEBREW GERESH: must be preceded by a Hebrew character.
		case r == '\u05F3':
			if i == 0 || !unicode.Is(unicode.Hebrew, runes[i-1]) {
				return errors.New("invalid hostname: HEBREW GERESH not preceded by Hebrew character")
			}

		// U+05F4 HEBREW GERSHAYIM: must be preceded by a Hebrew character.
		case r == '\u05F4':
			if i == 0 || !unicode.Is(unicode.Hebrew, runes[i-1]) {
				return errors.New("invalid hostname: HEBREW GERSHAYIM not preceded by Hebrew character")
			}

		// U+30FB KATAKANA MIDDLE DOT: label must contain ≥1 Hiragana/Katakana/Han.
		case r == '\u30FB':
			if !hasCJK {
				return errors.New("invalid hostname: KATAKANA MIDDLE DOT without Hiragana/Katakana/Han")
			}
		}
	}

	return nil
}

// isIDNA2008Permitted reports whether r may appear in a U-label under RFC
// 5892: an ASCII letter, digit, or hyphen; a code point whose general
// category the derived property admits (a lowercase, other, or modifier
// letter, a nonspacing or spacing mark, or a decimal digit); one of the
// section 2.6 PVALID exceptions; or a CONTEXTJ or CONTEXTO code point, whose
// rules run beside this check. An uppercase letter never reaches here, since
// the UTS 46 mapping folds case first. The general categories approximate
// the derived table: a mark in a block the table ignores (U+20D0 to U+20FF,
// say) passes here.
func isIDNA2008Permitted(r rune) bool {
	if r < utf8.RuneSelf {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-'
	}

	switch r {
	case '\u00DF', '\u03C2', '\u06FD', '\u06FE', '\u0F0B', '\u3007', // RFC 5892 section 2.6 PVALID exceptions
		'\u200C', '\u200D', // CONTEXTJ
		'\u00B7', '\u0375', '\u05F3', '\u05F4', '\u30FB': // CONTEXTO
		return true
	}

	return unicode.In(r, unicode.Ll, unicode.Lo, unicode.Lm, unicode.Mn, unicode.Mc, unicode.Nd)
}

// validateIDNEmail validates an internationalized email address per RFC 6531.
// It shares the email machinery the plain email format uses: splitEmail locates
// the local/domain boundary so a quoted local part containing '@' is honored,
// and a bracketed address literal in the domain follows the same path as
// validateEmailDomain. IDN-specific behavior is confined to where RFC 6531
// widens RFC 5321: UTF-8 in the local part and U-labels/IDNA in the hostname.
// The §4.5.3.1 size limits are not among the widenings -- RFC 6531 §3.4 keeps
// them and keeps them measured in octets -- so the 254-octet total applies here
// as it does to the plain email format.
func validateIDNEmail(s string) error {
	if len(s) > maxEmailOctets {
		return errors.New("invalid IDN email: address too long")
	}

	local, domain, ok := splitEmail(s)
	if !ok {
		return errors.New("invalid IDN email: missing or misplaced @")
	}

	err := validateIDNEmailLocal(local)
	if err != nil {
		return err
	}

	return validateIDNEmailDomain(domain)
}

// validateIDNEmailLocal validates the local part of an IDN email address
// (RFC 6531). Non-ASCII characters are permitted, but the part must be at most
// 64 octets and, unless quoted, must form a dot-atom of IDN atext runes. The
// quoted form reuses validateQuotedLocal with its Unicode widening enabled:
// the escape-aware scan rejects control characters, bare interior quotes, and
// an unterminated string, and additionally admits well-formed non-ASCII UTF-8
// in the unescaped text, which is the RFC 6531 widening over RFC 5321.
func validateIDNEmailLocal(s string) error {
	if s == "" {
		return errors.New("invalid IDN email: empty local part")
	}

	if len(s) > 64 {
		return errors.New("invalid IDN email: local part too long")
	}

	if s[0] == '"' {
		return validateQuotedLocal(s, true)
	}

	return validateDotAtom(s, isIDNAtext, dotAtomErrors{
		edgeDot:   "invalid IDN email: leading or trailing dot in local part",
		doubleDot: "invalid IDN email: consecutive dots in local part",
		badChar:   "invalid IDN email: invalid character in local part",
	})
}

// validateIDNEmailDomain validates the domain part of an IDN email address. A
// bracketed address literal ([IPv4] or [IPv6:...]) follows the same path as
// validateEmailDomain, since RFC 6531 inherits the RFC 5321 address-literal
// grammar unchanged. A hostname is validated as an internationalized domain
// name (U-labels/IDNA); the RFC 5321/6531 email domain grammar permits an
// all-numeric top-level label, so the idn-hostname numeric-TLD ban does not
// apply here, and it has no trailing-dot production, so the FQDN root-dot
// allowance does not apply either. An ASCII label without an ACE prefix is held
// to RFC 5321 sub-domain rather than to IDNA, because RFC 6531 §3.3 widens the
// RFC 5321 Domain grammar by admitting U-labels and leaves its ASCII
// alternative alone; the plain email format accepts the same labels.
func validateIDNEmailDomain(d string) error {
	if d == "" {
		return errors.New("invalid IDN email: empty domain")
	}

	if strings.HasPrefix(d, "[") && strings.HasSuffix(d, "]") {
		return validateEmailDomain(d)
	}

	return validateIDNHostnameLabels(d, false, false, true)
}

// isIDNAtext reports whether r may appear in an unquoted IDN email local part
// (RFC 6531). It widens RFC 5321 atext with UTF8-non-ascii (RFC 6532 §3.1),
// which admits every non-ASCII code point -- including non-ASCII whitespace
// and C1 controls, with no carve-out -- while still rejecting ASCII characters
// that atext disallows (such as whitespace, control characters, and specials
// like '"', '(', '\\', and ','). Sequence well-formedness is the dot-atom
// scanner's job, so a decoded rune here is already from a well-formed
// sequence; the quoted-local scan applies the same widening.
func isIDNAtext(r rune) bool {
	if r > unicode.MaxASCII {
		return true
	}

	return isAtext(r)
}
