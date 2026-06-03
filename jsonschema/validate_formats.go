package jsonschema

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"

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

	// Split on T separator (RFC 3339 allows lowercase t).
	datePart, timePart, ok := strings.Cut(upper, "T")
	if !ok {
		return errors.New("invalid date-time")
	}

	_, err := time.Parse("2006-01-02", datePart)
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

// utcOffsetMinutes returns the signed minute offset encoded in a time string's
// trailing zone designator: positive for "+hh:mm", negative for "-hh:mm". A
// trailing "Z" or an absent/malformed offset yields 0. Converting a local time
// to UTC subtracts this value.
func utcOffsetMinutes(s string) int {
	if strings.HasSuffix(s, "Z") {
		return 0
	}

	idx := strings.LastIndexAny(s, "+-")
	if idx < 0 {
		return 0
	}

	offset := s[idx:]
	if len(offset) != 6 {
		return 0
	}

	offHour := int(offset[1]-'0')*10 + int(offset[2]-'0')
	offMinute := int(offset[4]-'0')*10 + int(offset[5]-'0')
	total := offHour*60 + offMinute

	if offset[0] == '-' {
		return -total
	}

	return total
}

// validateTimeOffset checks that a time zone offset has valid hour (<24) and
// minute (<60) components.
func validateTimeOffset(s string) error {
	if strings.HasSuffix(s, "Z") {
		return nil
	}

	idx := strings.LastIndexAny(s, "+-")
	if idx < 0 {
		return nil
	}

	offset := s[idx:]
	if len(offset) != 6 || offset[3] != ':' {
		return errors.New("invalid time offset")
	}

	hour := int(offset[1]-'0')*10 + int(offset[2]-'0')
	minute := int(offset[4]-'0')*10 + int(offset[5]-'0')
	if hour > 23 || minute > 59 {
		return errors.New("invalid time offset")
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

// validateDotAtomLocal validates a dot-atom local part: atext runs separated by
// single dots, with no leading, trailing, or consecutive dots.
func validateDotAtomLocal(s string) error {
	if s[0] == '.' || s[len(s)-1] == '.' || strings.Contains(s, "..") {
		return errors.New("invalid email: misplaced dot in local part")
	}

	for _, r := range s {
		if r == '.' || isAtext(r) {
			continue
		}

		return errors.New("invalid email: invalid character in local part")
	}

	return nil
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

	return validateHostname(d)
}

func validateHostname(s string) error {
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

		// RFC 5891 §4.2.3.1: labels with hyphens at positions 3-4 are
		// reserved for IDNA (e.g. "xn--"). Validate as A-label.
		if len(label) >= 4 && label[2] == '-' && label[3] == '-' {
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

	// The top-level label must not be all-numeric, to disambiguate from an
	// IPv4 address (RFC 1123 §2.1).
	if isAllDigits(labels[len(labels)-1]) {
		return errors.New("invalid hostname: numeric top-level label")
	}

	return nil
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
	// Three or more consecutive slashes after the authority indicate a
	// malformed authority/path boundary (e.g. "http://host///path"). A single
	// extra slash ("http://host//path") is a valid empty path segment and is
	// allowed.
	if strings.HasPrefix(u.Path, "///") {
		return errors.New("invalid URI: malformed path")
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

// containsInvalidURIChars checks for characters forbidden by RFC 3986.
func containsInvalidURIChars(s string) bool {
	for _, c := range s {
		if c > 0x7E || c == ' ' || c == '<' || c == '>' ||
			c == '{' || c == '}' || c == '^' || c == '`' ||
			c == '|' || c == '"' || c == '\\' {
			return true
		}
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

			err := validateRegexEscape(s[i+1])
			if err != nil {
				return err
			}

			i += 2

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
// 262 does not define (e.g. "\a"), which RE2 would otherwise accept.
func validateRegexEscape(c byte) error {
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
// Unlike URIs, IRIs allow non-ASCII Unicode characters.
func containsInvalidIRIChars(s string) bool {
	for _, c := range s {
		if c == ' ' || c == '<' || c == '>' ||
			c == '{' || c == '}' || c == '^' || c == '`' ||
			c == '|' || c == '\\' || c == '"' {
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

// validateURITemplateExpr validates the contents of a single {expression}.
func validateURITemplateExpr(e string) error {
	if e == "" {
		return errors.New("invalid URI template: empty expression")
	}

	for i := range len(e) {
		if !isURITemplateExprChar(e[i]) {
			return errors.New("invalid URI template: invalid character in expression")
		}
	}

	return nil
}

// isURITemplateExprChar reports whether c may appear inside a URI Template
// expression: variable characters, the level-2/3 operators, and modifiers.
func isURITemplateExprChar(c byte) bool {
	if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
		return true
	}

	switch c {
	case '_', '.', ',', '*', ':', '%', '+', '#', '/', ';', '?', '&', '=', '!', '@', '|':
		return true
	}

	return false
}

// validateIDNHostname validates an internationalized hostname per RFC 5890/5891.
func validateIDNHostname(s string) error {
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
			start = i + len(string(r))
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
func validateIDNEmail(s string) error {
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 {
		return errors.New("invalid IDN email: missing or misplaced @")
	}

	local := s[:at]
	domain := s[at+1:]

	err := validateIDNEmailLocal(local)
	if err != nil {
		return err
	}

	return validateIDNHostname(domain)
}

// validateIDNEmailLocal validates the local part of an IDN email address
// (RFC 6531). Non-ASCII characters are permitted, but the part must be at most
// 64 octets and, unless quoted, must form a dot-atom of IDN atext runes.
func validateIDNEmailLocal(s string) error {
	if s == "" {
		return errors.New("invalid IDN email: empty local part")
	}
	if len(s) > 64 {
		return errors.New("invalid IDN email: local part too long")
	}

	// A leading quote signals a quoted-string local part, which may contain
	// spaces and dots. It must be terminated by a matching closing quote;
	// otherwise the local part is malformed (an unbalanced or lone quote must
	// not fall through to the dot-atom checks below).
	if s[0] == '"' {
		if len(s) < 2 || s[len(s)-1] != '"' {
			return errors.New("invalid IDN email: malformed quoted local part")
		}

		return nil
	}

	if s[0] == '.' || s[len(s)-1] == '.' {
		return errors.New("invalid IDN email: leading or trailing dot in local part")
	}
	if strings.Contains(s, "..") {
		return errors.New("invalid IDN email: consecutive dots in local part")
	}

	for _, r := range s {
		if r == '.' || isIDNAtext(r) {
			continue
		}

		return errors.New("invalid IDN email: invalid character in local part")
	}

	return nil
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
