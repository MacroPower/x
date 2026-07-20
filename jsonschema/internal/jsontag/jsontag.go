// Package jsontag parses the encoding/json struct tag (the `json:"..."` tag),
// reporting the JSON field name and the option flags encoding/json recognizes.
// It models only the standard library's name/option grammar; the jsonschema:
// constraint DSL is parsed elsewhere.
package jsontag

import (
	"reflect"
	"strings"
	"unicode"
)

// Info is the parsed result of a field's json struct tag.
type Info struct {
	// JSONName is the field's JSON name; empty means the field is excluded
	// (json:"-" or an unexported, non-embedded field).
	JSONName string
	// Omitempty reports the ",omitempty" option.
	Omitempty bool
	// Omitzero reports the ",omitzero" option.
	Omitzero bool
	// JSONString reports the ",string" option.
	JSONString bool
	// TaggedName is true when JSONName comes from an explicit json tag name
	// rather than the Go field name. Encoding/json's same-depth collision
	// tie-break keeps a field only when exactly one colliding field is tagged.
	TaggedName bool
}

// Parse parses the json struct tag of f.
func Parse(f reflect.StructField) Info {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		// Use field name if no tag.
		if !f.IsExported() && !f.Anonymous {
			return Info{} // excluded
		}

		// The field name serves embedded fields too: for both value and pointer
		// embeds it is the unqualified type identifier, without the type
		// arguments [reflect.Type.Name] carries for an instantiated generic
		// type, exactly the key encoding/json uses.
		return Info{JSONName: f.Name}
	}

	name, rest, found := strings.Cut(tag, ",")
	if name == "-" && !found {
		return Info{} // excluded
	}

	// Encoding/json discards a name it rejects as invalid, falling back to the
	// field name and treating the field as untagged.
	if !ValidName(name) {
		name = ""
	}

	// A non-empty name segment is an explicit json tag name; an options-only tag
	// (json:",omitempty") leaves the name empty and falls back to the field name,
	// which is not "tagged" for the same-depth collision tie-break.
	taggedName := name != ""

	if name == "" {
		// Use the field name, which for an embedded field is the unqualified
		// type identifier encoding/json keys the field by.
		name = f.Name
	}

	info := Info{JSONName: name, TaggedName: taggedName}
	if found {
		for s := range strings.SplitSeq(rest, ",") {
			switch s {
			case "omitempty":
				info.Omitempty = true
			case "omitzero":
				info.Omitzero = true
			case "string":
				info.JSONString = true
			}
		}
	}

	return info
}

// ValidName reports whether a json tag name is one encoding/json honors:
// letters, digits, and a fixed punctuation set (backslash and quote characters
// are reserved). An invalid name is discarded and the field falls back to its
// Go name, exactly as encoding/json's isValidTag does; the empty string is
// invalid.
func ValidName(name string) bool {
	if name == "" {
		return false
	}

	for _, c := range name {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
			// Any punctuation from the fixed set is allowed in a tag name.
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}

	return true
}
