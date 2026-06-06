package jsonschema

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

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
)

func validateDateTime(s string) error {
	upper := strings.ToUpper(s)

	// Split on T separator (RFC 3339 allows lowercase t). Uppercasing first
	// folds the lowercase 't' so the date and time halves split cleanly; the
	// date half holds only digits and hyphens, so uppercasing leaves it
	// unchanged before it is handed to validateDate.
	datePart, timePart, ok := strings.Cut(upper, "T")
	if !ok {
		return errors.New("invalid date-time")
	}

	err := validateDate(datePart)
	if err != nil {
		return errors.New("invalid date-time")
	}

	return validateTime(timePart)
}

func validateDate(s string) error {
	_, err := time.Parse("2006-01-02", s)
	if err != nil {
		return errors.New("invalid date")
	}

	return nil
}

func validateTime(s string) error {
	upper := strings.ToUpper(s)

	// RFC 3339 partial-time requires a two-digit hour, minute, and second.
	// Go's time.Parse accepts a single-digit hour for the "15" layout field, so
	// enforce the fixed-width "hh:mm:ss" shape explicitly; this also keeps the
	// fixed byte offsets in validateLeapSecond aligned.
	if !hasTwoDigitClock(upper) {
		return errors.New("invalid time")
	}

	// Handle leap second: temporarily replace :60 with :59 for parsing.
	isLeap := false
	normalized := upper
	if i := strings.Index(upper, ":60"); i >= 3 {
		isLeap = true
		normalized = upper[:i+1] + "59" + upper[i+3:]
	}

	_, err := time.Parse("15:04:05Z07:00", normalized)
	if err != nil {
		_, err = time.Parse("15:04:05.999999999Z07:00", normalized)
	}

	if err != nil {
		return errors.New("invalid time")
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
		return errors.New("invalid time offset")
	case offsetNumeric:
		if off.hour > 23 || off.minute > 59 {
			return errors.New("invalid time offset")
		}
	}

	return nil
}

func validateEmail(s string) error {
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
		return validateQuotedLocal(s)
	}

	return validateDotAtomLocal(s)
}

// validateQuotedLocal validates a quoted-string local part.
func validateQuotedLocal(s string) error {
	if len(s) < 2 || s[len(s)-1] != '"' {
		return errors.New("invalid email: malformed quoted local part")
	}

	inner := s[1 : len(s)-1]
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c == '\\' {
			i++ // skip the escaped character
			continue
		}

		if c < 0x20 || c == 0x7F {
			return errors.New("invalid email: control character in local part")
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

	for _, r := range s {
		if r == '.' || isText(r) {
			continue
		}

		return errors.New(msgs.badChar)
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
		if rest, found := strings.CutPrefix(lit, "IPv6:"); found {
			return validateIPv6(rest)
		}

		return validateIPv4(lit)
	}

	// Email domains follow the RFC 5321 sub-domain grammar
	// (sub-domain = Let-dig [Ldh-str]), which permits an all-numeric top-level
	// label, so the numeric-TLD ban from the hostname format must not apply.
	return validateHostnameLabels(d, false)
}

func validateHostname(s string) error {
	// The hostname format is RFC 1123-based; the top-level label must not be
	// all-numeric, to disambiguate from an IPv4 address (RFC 1123 §2.1).
	return validateHostnameLabels(s, true)
}

// validateHostnameLabels validates the shared RFC 1123 label structure used by
// both the hostname format and email domain validation. The banNumericTLD flag
// rejects an all-numeric top-level label, which the hostname format requires
// (RFC 1123 §2.1) but the RFC 5321 email domain grammar permits.
func validateHostnameLabels(s string, banNumericTLD bool) error {
	if s == "" || len(s) > 253 {
		return errors.New("invalid hostname")
	}

	// Allow a single trailing dot on a multi-label FQDN (e.g. "example.com."),
	// but reject a bare trailing dot like "example." or ".".
	if strings.HasSuffix(s, ".") {
		trimmed := s[:len(s)-1]
		if !strings.Contains(trimmed, ".") {
			return errors.New("invalid hostname")
		}

		s = trimmed
	}

	labels := strings.Split(s, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return errors.New("invalid hostname")
		}

		for i, c := range label {
			isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			isDigit := c >= '0' && c <= '9'
			isHyphen := c == '-' && i > 0 && i < len(label)-1
			if !isAlpha && !isDigit && !isHyphen {
				return errors.New("invalid hostname")
			}
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
				return errors.New("invalid hostname")
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

func validateURI(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("invalid URI")
	}

	if u.Scheme == "" {
		return errors.New("invalid URI: missing scheme")
	}

	if containsInvalidURIChars(s) {
		return errors.New("invalid URI: forbidden characters")
	}

	// Bare IPv6 addresses must be enclosed in brackets per RFC 3986 §3.2.2,
	// mirroring validateIRI so "uri" and "iri" agree on this case.
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return errors.New("invalid URI: bare IPv6 address")
	}

	return nil
}

func validateURIReference(s string) error {
	_, err := url.Parse(s)
	if err != nil {
		return errors.New("invalid URI reference")
	}

	if containsInvalidURIChars(s) {
		return errors.New("invalid URI reference: forbidden characters")
	}

	return nil
}

// containsInvalidURIChars checks for characters forbidden by RFC 3986. A URI is
// limited to ASCII, so any code point above '~' (0x7E) is also rejected.
func containsInvalidURIChars(s string) bool {
	for _, c := range s {
		if c > 0x7E || isForbiddenURIIRIChar(c) {
			return true
		}
	}

	return false
}

// isForbiddenURIIRIChar reports whether c is one of the gen-delims/sub-delims
// and other characters that both RFC 3986 (URI) and RFC 3987 (IRI) exclude from
// the unreserved/reserved sets. It covers only the rules the two share; their
// genuinely different rule (URIs additionally ban all non-ASCII) lives in
// containsInvalidURIChars.
func isForbiddenURIIRIChar(c rune) bool {
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
		return errors.New("invalid UUID")
	}

	for i := range 36 {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return errors.New("invalid UUID")
			}

		default:
			if !isHexDigit(s[i]) {
				return errors.New("invalid UUID")
			}
		}
	}

	return nil
}

func validateIPv4(s string) error {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return errors.New("invalid IPv4 address")
	}

	// Ensure it's actually written in dotted-decimal, not ::ffff:a.b.c.d.
	if strings.Contains(s, ":") {
		return errors.New("invalid IPv4 address")
	}

	return nil
}

func validateIPv6(s string) error {
	ip := net.ParseIP(s)
	if ip == nil {
		return errors.New("invalid IPv6 address")
	}

	// Must contain a colon to be IPv6.
	if !strings.Contains(s, ":") {
		return errors.New("invalid IPv6 address")
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

// validateRegex checks that s is a valid ECMA 262 regular expression. The
// "regex" format is defined in terms of ECMA 262, which is a superset of Go's
// RE2 (it permits backreferences and lookaround). A structural check is used
// rather than [regexp.Compile] so valid ECMA 262 patterns that RE2 rejects are
// still accepted, while genuinely malformed patterns are rejected.
func validateRegex(s string) error {
	depth := 0

	inClass := false
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

			continue

		case inClass:
			if c == ']' {
				inClass = false
			}

		case c == '[':
			inClass = true
		case c == '(':
			depth++
		case c == ')':
			if depth == 0 {
				return errors.New("invalid regex: unbalanced parenthesis")
			}

			depth--
		}

		i++
	}

	if depth != 0 {
		return errors.New("invalid regex: unbalanced parenthesis")
	}

	if inClass {
		return errors.New("invalid regex: unterminated character class")
	}

	return nil
}

// validateRegexEscape reports whether c is a valid character following a
// backslash in an ECMA 262 regular expression. It rejects escapes that ECMA
// 262 does not define (e.g. "\a"), which RE2 would otherwise accept. Any
// non-ASCII rune is a valid identity escape: ECMA 262 Annex B (non-unicode
// mode) permits an identity escape of any source character that is not part of
// another escape, so an escaped multi-byte code point is accepted. The size is
// the byte length the rune decoded from: a lone invalid UTF-8 byte decodes to
// (utf8.RuneError, 1) and is rejected, while a genuine U+FFFD decodes from three
// bytes and is accepted as an identity escape.
func validateRegexEscape(c rune, size int) error {
	if c == utf8.RuneError && size == 1 {
		return errors.New("invalid regex: invalid escape sequence")
	}

	if c >= utf8.RuneSelf {
		return nil
	}

	switch {
	case c >= '0' && c <= '9': // backreference / octal
		return nil
	case c == 'd' || c == 'D' || c == 'w' || c == 'W' || c == 's' || c == 'S':
		return nil
	case c == 'b' || c == 'B': // word boundary / backspace
		return nil
	case c == 'n' || c == 'r' || c == 't' || c == 'f' || c == 'v':
		return nil
	case c == 'x' || c == 'u' || c == 'c': // hex / unicode / control escapes
		return nil
	case c == 'k' || c == 'p' || c == 'P': // named backref / unicode property
		return nil
	}

	switch c {
	case '^', '$', '\\', '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|', '/', '-':
		return nil
	}

	return errors.New("invalid regex: invalid escape sequence")
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
		return errors.New("invalid duration: no components")
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
		return errors.New("invalid duration: no components")
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
// or reordering). This enforces the RFC 3339 ABNF nesting — for example
// dur-year = nY [dur-month] — so a sequence like P1Y2D (year then day, skipping
// month) is rejected. It returns the designator's order index.
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

// validateIRI validates an IRI per RFC 3987. IRIs allow non-ASCII Unicode
// characters but otherwise follow URI structure (must have a scheme).
func validateIRI(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("invalid IRI")
	}

	if u.Scheme == "" {
		return errors.New("invalid IRI: missing scheme")
	}

	if containsInvalidIRIChars(s) {
		return errors.New("invalid IRI: forbidden characters")
	}

	// Bare IPv6 addresses must be enclosed in brackets per RFC 3986 §3.2.2.
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return errors.New("invalid IRI: bare IPv6 address")
	}

	return nil
}

// validateIRIReference validates an IRI-reference per RFC 3987. Same as IRI
// but allows relative references (no scheme requirement).
func validateIRIReference(s string) error {
	_, err := url.Parse(s)
	if err != nil {
		return errors.New("invalid IRI reference")
	}

	if containsInvalidIRIChars(s) {
		return errors.New("invalid IRI reference: forbidden characters")
	}

	return nil
}

// containsInvalidIRIChars checks for characters forbidden by RFC 3987.
// Unlike URIs, IRIs allow non-ASCII Unicode characters, so only the shared
// forbidden set applies.
func containsInvalidIRIChars(s string) bool {
	for _, c := range s {
		if isForbiddenURIIRIChar(c) {
			return true
		}
	}

	return false
}

// validateURITemplate validates a URI Template per RFC 6570. It checks for
// matched, non-nested braces and that each brace expression is non-empty and
// contains only valid expression characters.
func validateURITemplate(s string) error {
	inExpr := false

	exprStart := 0
	for i := range len(s) {
		switch s[i] {
		case '{':
			if inExpr {
				return errors.New("invalid URI template: nested brace")
			}

			inExpr = true
			exprStart = i + 1

		case '}':
			if !inExpr {
				return errors.New("invalid URI template: unmatched closing brace")
			}

			err := validateURITemplateExpr(s[exprStart:i])
			if err != nil {
				return err
			}

			inExpr = false
		}
	}

	if inExpr {
		return errors.New("invalid URI template: unmatched opening brace")
	}

	return nil
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
	// plain hostname format so it cannot be confused with an IPv4 address.
	return validateIDNHostnameLabels(s, true)
}

// validateIDNHostnameLabels validates the shared RFC 5890/5891 label structure
// used by both the idn-hostname format and idn-email domain validation. The
// banNumericTLD flag rejects an all-numeric top-level label, which the
// idn-hostname format requires but the RFC 5321/6531 email domain grammar
// permits.
func validateIDNHostnameLabels(s string, banNumericTLD bool) error {
	if s == "" {
		return errors.New("invalid IDN hostname: empty string")
	}

	labels := splitIDNADots(s)

	// Allow a single trailing dot on a multi-label FQDN (e.g. "example.com."),
	// consistent with validateHostname. A bare trailing dot on a single label
	// ("example.") still leaves an empty label and is rejected below.
	if n := len(labels); n >= 3 && labels[n-1] == "" {
		labels = labels[:n-1]
	}

	totalLen := 0
	for i, label := range labels {
		if label == "" {
			return errors.New("invalid IDN hostname: empty label")
		}

		ascii, err := idna.Lookup.ToASCII(label)
		if err != nil {
			return errors.New("invalid IDN hostname: " + err.Error())
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

		err = checkContextualRules(label)
		if err != nil {
			return errors.New("invalid IDN hostname: " + err.Error())
		}

		totalLen += len(ascii)
		if i > 0 {
			totalLen++ // dot separator
		}
	}

	// RFC 5890: the full domain name in A-label form must not exceed 253 octets.
	if totalLen > 253 {
		return errors.New("invalid IDN hostname: name too long")
	}

	// When banNumericTLD is set, the top-level label must not be all-numeric,
	// mirroring validateHostname so that an idn-hostname cannot be confused with
	// an IPv4 address (RFC 1123 §2.1 / RFC 5890). The check applies to the label
	// as written: a numeric U-label is ASCII digits, so an all-ASCII-digit final
	// label is rejected.
	if banNumericTLD && isAllDigits(labels[len(labels)-1]) {
		return errors.New("invalid IDN hostname: numeric top-level label")
	}

	return nil
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

// validateIDNEmail validates an internationalized email address per RFC 6531.
// It shares the email machinery the plain email format uses: splitEmail locates
// the local/domain boundary so a quoted local part containing '@' is honored,
// and a bracketed address literal in the domain follows the same path as
// validateEmailDomain. IDN-specific behavior is confined to where RFC 6531
// widens RFC 5321: UTF-8 in the local part and U-labels/IDNA in the hostname.
func validateIDNEmail(s string) error {
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
// quoted form reuses validateQuotedLocal, whose escape-aware scan rejects
// control characters, bare interior quotes, and an unterminated string; that
// scan already admits non-ASCII UTF-8 (its bytes are all >= 0x80, above the
// rejected control range), which is the RFC 6531 widening over RFC 5321.
func validateIDNEmailLocal(s string) error {
	if s == "" {
		return errors.New("invalid IDN email: empty local part")
	}

	if len(s) > 64 {
		return errors.New("invalid IDN email: local part too long")
	}

	if s[0] == '"' {
		return validateQuotedLocal(s)
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
// apply here.
func validateIDNEmailDomain(d string) error {
	if d == "" {
		return errors.New("invalid IDN email: empty domain")
	}

	if strings.HasPrefix(d, "[") && strings.HasSuffix(d, "]") {
		return validateEmailDomain(d)
	}

	return validateIDNHostnameLabels(d, false)
}

// isIDNAtext reports whether r may appear in an unquoted IDN email local part
// (RFC 6531). It widens RFC 5321 atext with non-ASCII Unicode code points,
// while still rejecting ASCII characters that atext disallows (such as
// whitespace, control characters, and specials like '"', '(', '\\', and ',').
func isIDNAtext(r rune) bool {
	if r > unicode.MaxASCII {
		return !unicode.IsControl(r) && !unicode.IsSpace(r)
	}

	return isAtext(r)
}
