// Package jsontag parses the encoding/json struct tag (the `json:"..."` tag),
// reporting the JSON field name and the option flags encoding/json recognizes.
// It models only the standard library's name/option grammar; the jsonschema:
// constraint DSL is parsed elsewhere.
package jsontag

import (
	"reflect"
	"strings"
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

		name := f.Name
		// For embedded non-struct types, use the type name.
		if f.Anonymous {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}

			name = ft.Name()
		}

		return Info{JSONName: name}
	}

	name, rest, found := strings.Cut(tag, ",")
	if name == "-" && !found {
		return Info{} // excluded
	}

	// A non-empty name segment is an explicit json tag name; an options-only tag
	// (json:",omitempty") leaves the name empty and falls back to the field name,
	// which is not "tagged" for the same-depth collision tie-break.
	taggedName := name != ""

	if name == "" {
		// Use field name.
		name = f.Name
		if f.Anonymous {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}

			name = ft.Name()
		}
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
