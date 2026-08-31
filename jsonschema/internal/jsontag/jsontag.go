// Package jsontag parses the encoding/json struct tag (the `json:"..."` tag),
// reporting the JSON field name and the option flags [encoding/json/v2]
// recognizes. It models only the standard library's name/option grammar; the
// jsonschema: constraint DSL is parsed elsewhere.
//
// The grammar mirrored here is the one Go 1.27's encoding/json/v2 implements
// (parseFieldOptions in encoding/json/v2, which also backs the v1 API),
// including the parse errors v2 reports when marshaling a type whose tags are
// malformed. [Parse] returns the first such error beside the Info the rest of
// the tag yields, matching v2, which keeps parsing after an error so a struct
// reports one fault at a time.
package jsontag

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// reservedRunes are the runes that terminate a json tag name: the comma
// delimits options, and backslash and the quote characters are reserved by
// the tag grammar.
const reservedRunes = ",\\'\"`"

// Case values for the ",case:..." tag option.
const (
	// CaseIgnore is the "case:ignore" option: unmarshaling matches the member
	// name case-insensitively.
	CaseIgnore = "ignore"
	// CaseStrict is the "case:strict" option: unmarshaling matches the member
	// name case-sensitively even under MatchCaseInsensitiveNames.
	CaseStrict = "strict"
)

// Info is the parsed result of a field's json struct tag.
type Info struct {
	// JSONName is the property name the field marshals under: the tag's name
	// when it sets one, the Go field name otherwise, and empty for a field
	// encoding/json excludes (json:"-", or an unexported non-embedded field).
	JSONName string
	// Case holds the case tag option's value, [CaseIgnore] or [CaseStrict].
	Case string
	// Format holds the format tag option's value. Stable encoding/json/v2
	// defines no values for it on a struct field, so any appearance is a
	// marshal-time error the caller reports.
	Format string
	// Omitempty reports the ",omitempty" option.
	Omitempty bool
	// Omitzero reports the ",omitzero" option.
	Omitzero bool
	// JSONString reports the ",string" option.
	JSONString bool
	// TaggedName reports that JSONName came from an explicit tag name rather
	// than the Go field name, the input to encoding/json's tie-break for
	// fields colliding on a JSON name at one depth.
	TaggedName bool
	// Embed reports the ",embed" option, which gives a named field promotion
	// semantics.
	Embed bool
}

// Parse parses the json struct tag of f. The returned error is the first
// fault encoding/json/v2 would report for the tag when marshaling; the Info
// beside it holds what the rest of the tag yields, so a caller that surfaces
// the error can still describe the field.
func Parse(f reflect.StructField) (Info, error) {
	tag, hasTag := f.Tag.Lookup("json")

	if tag == "-" {
		return Info{}, nil // excluded; "-," names a literal "-" below
	}

	// Encoding/json skips an unexported non-embedded field before reading its
	// tag, so a json tag cannot resurrect it -- and v2 reports a non-ignored
	// tag on such a field as an error.
	if !f.IsExported() && !f.Anonymous {
		var err error

		if hasTag {
			err = fmt.Errorf(
				"unexported Go struct field %s cannot have non-ignored `json:%q` tag",
				f.Name, tag,
			)
		}

		return Info{}, err
	}

	if !hasTag {
		// The field name serves embedded fields too: for both value and pointer
		// embeds it is the unqualified type identifier, without the type
		// arguments [reflect.Type.Name] carries for an instantiated generic
		// type, exactly the key encoding/json uses.
		return Info{JSONName: f.Name}, nil
	}

	var firstErr error

	keepErr := func(err error) {
		if firstErr == nil && err != nil {
			firstErr = err
		}
	}

	info := Info{JSONName: f.Name}

	if tag != "" && !strings.HasPrefix(tag, ",") {
		tag = parseName(f, tag, &info, keepErr)
	}

	var (
		wasFormat bool
		// The seen set is allocated on the first option, so the common
		// options-free tag pays nothing for the duplicate check.
		seen map[string]bool
	)

	for tag != "" {
		if tag[0] != ',' {
			keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
				"Go struct field %s has malformed `json` tag: invalid character %q before next option (expecting ',')",
				f.Name, rune(tag[0]),
			))
		} else {
			tag = tag[1:]
			if tag == "" {
				keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
					"Go struct field %s has malformed `json` tag: invalid trailing ',' character",
					f.Name,
				))

				break
			}
		}

		opt, n, errOpt := consumeTagOption(tag)
		if errOpt != nil {
			keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
				"Go struct field %s has malformed `json` tag: %w", f.Name, errOpt,
			))
		}

		tag = tag[n:]

		if wasFormat {
			keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
				"Go struct field %s has `format` tag option that was not specified last",
				f.Name,
			))
		}

		switch opt {
		case "case":
			if !strings.HasPrefix(tag, ":") {
				keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
					"Go struct field %s is missing value for `case` tag option; specify `case:ignore` or `case:strict` instead",
					f.Name,
				))

				break
			}

			tag = tag[1:]

			val, vn, errVal := consumeTagOption(tag)
			if errVal != nil {
				keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
					"Go struct field %s has malformed value for `case` tag option: %w",
					f.Name, errVal,
				))

				break
			}

			tag = tag[vn:]

			switch val {
			case CaseIgnore, CaseStrict:
				// Both together is the duplicate-family fault v2 reports.
				if info.Case != "" && info.Case != val {
					keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
						"Go struct field %s cannot have both `case:ignore` and `case:strict` tag options",
						f.Name,
					))
				}

				info.Case = val

			default:
				keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
					"Go struct field %s has unknown `case:%s` tag value", f.Name, val,
				))
			}

		case "embed":
			info.Embed = true
		case "omitzero":
			info.Omitzero = true
		case "omitempty":
			info.Omitempty = true
		case "string":
			info.JSONString = true
		case "format":
			if !strings.HasPrefix(tag, ":") {
				keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
					"Go struct field %s is missing value for `format` tag option", f.Name,
				))

				break
			}

			tag = tag[1:]

			val, vn, errVal := consumeFormatValue(tag)

			switch {
			case errVal != nil:
				keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
					"Go struct field %s has malformed value for `format` tag option: %w",
					f.Name, errVal,
				))

			case val == "":
				keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
					"Go struct field %s cannot have empty value for `format` tag option",
					f.Name,
				))

			default:
				tag = tag[vn:]
				info.Format = val
				wasFormat = true
			}

		default:
			// Keys that resemble a supported option are invalid mutants such
			// as "omitEmpty" or "omit_empty".
			normOpt := strings.ReplaceAll(strings.ToLower(opt), "_", "")
			switch normOpt {
			case "case", "embed", "omitzero", "omitempty", "string", "format":
				keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
					"Go struct field %s has invalid appearance of `%s` tag option; specify `%s` instead",
					f.Name, opt, normOpt,
				))
			}
		}

		if seen[opt] {
			keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
				"Go struct field %s has duplicate appearance of `%s` tag option",
				f.Name, opt,
			))
		}

		if seen == nil {
			seen = make(map[string]bool)
		}

		seen[opt] = true
	}

	return info, firstErr
}

// parseName consumes the tag's leading name, records it on info, and returns
// the rest of the tag. Faults go through keepErr, mirroring what
// encoding/json/v2 reports for the same tag.
func parseName(f reflect.StructField, tag string, info *Info, keepErr func(error)) string {
	// The name is the run of unreserved runes: any UTF-8, letters or not.
	n := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
		return !strings.ContainsRune(reservedRunes, r)
	}))
	name := tag[:n]

	// A name cut short by a reserved rune other than the comma is
	// malformed; encoding/json then retries the chunk as an option,
	// keeping a leading identifier as the name and otherwise reporting
	// the malformed tag and falling back to the Go field name.
	var errName error

	if !strings.HasPrefix(tag[n:], ",") && len(name) != len(tag) {
		name, n, errName = consumeTagOption(tag)
		if errName != nil {
			keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
				"Go struct field %s has malformed `json` tag: %w", f.Name, errName,
			))
		}
	}

	if !utf8.ValidString(name) {
		keepErr(fmt.Errorf( //nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
			"Go struct field %s has JSON object name %q with invalid UTF-8",
			f.Name, name,
		))

		// The name is kept, with each invalid byte replaced by the Unicode
		// replacement character.
		name = string([]rune(name))
	}

	if errName == nil {
		info.JSONName = name
		info.TaggedName = true
	}

	return tag[n:]
}

// consumeTagOption consumes the next tag option, mirroring encoding/json/v2's
// consumeTagOption with quoting disallowed (the stable grammar allows a
// single-quoted token only as a format value): a chunk starting with a letter
// or underscore yields its identifier prefix, and any other chunk is skipped
// to the next comma and reported with the fault v2 names.
func consumeTagOption(in string) (string, int, error) {
	return consumeToken(in, false)
}

// consumeFormatValue consumes a format option value, which unlike every other
// token may be single-quoted (`format:'2006-01-02'`). The grammar converts
// the single-quoted form to a double-quoted Go string literal and unquotes
// it, exactly as encoding/json/v2 does.
func consumeFormatValue(in string) (string, int, error) {
	return consumeToken(in, true)
}

// consumeToken is the shared body of [consumeTagOption] and
// [consumeFormatValue]; allowQuoted is the one point the two grammars differ.
func consumeToken(in string, allowQuoted bool) (string, int, error) {
	i := strings.IndexByte(in, ',')
	if i < 0 {
		i = len(in)
	}

	switch r, _ := utf8.DecodeRuneInString(in); {
	case r == '_' || unicode.IsLetter(r):
		n := len(in) - len(strings.TrimLeftFunc(in, isLetterOrDigit))

		return in[:n], n, nil

	case allowQuoted && r == '\'':
		return consumeQuoted(in, i)
	case in == "":
		return in[:i], i, io.ErrUnexpectedEOF
	default:
		expecting := "Unicode letter"
		if allowQuoted {
			expecting = "Unicode letter or single quote"
		}

		return in[:i], i, fmt.Errorf(
			"invalid character %q at start of option (expecting %s)", r, expecting,
		)
	}
}

// consumeQuoted consumes a single-quoted token starting at a single quote. The
// fallback index i (the next comma or end of input) is reported with any
// error, mirroring v2.
func consumeQuoted(in string, i int) (string, int, error) {
	var inEscape bool

	b := []byte{'"'}
	n := len(`'`)

	for len(in) > n {
		r, rn := utf8.DecodeRuneInString(in[n:])
		switch {
		case inEscape:
			if r == '\'' {
				b = b[:len(b)-1] // remove escape character: `\'` => `'`
			}

			inEscape = false

		case r == '\\':
			inEscape = true
		case r == '"':
			b = append(b, '\\') // insert escape character: `"` => `\"`
		case r == '\'':
			b = append(b, '"')
			n += len(`'`)

			out, err := strconv.Unquote(string(b))
			if err != nil {
				return in[:i], i, fmt.Errorf("invalid single-quoted string: %s", in[:n])
			}

			return out, n, nil
		}

		b = append(b, in[n:][:rn]...)
		n += rn
	}

	if n > 10 {
		n = 10 // limit the amount of context printed in the error
	}

	//nolint:staticcheck // Matches encoding/json/v2's error text verbatim.
	return in[:i], i, fmt.Errorf("single-quoted string not terminated: %s...", in[:n])
}

// isLetterOrDigit reports whether r may appear in a tag option identifier.
func isLetterOrDigit(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}
