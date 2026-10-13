package tagparse

import (
	"errors"
	"fmt"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
)

// errDeprecationConflict reports a deprecationMessage pair beside a
// deprecated=false pair, which state two opposite intentions.
var errDeprecationConflict = errors.New(
	"jsonschema tag: key \"deprecationMessage\" deprecates the field that deprecated=false keeps current",
)

// Deprecation is the deprecation a jsonschema tag states for its field,
// through a deprecated pair, a deprecationMessage pair, or both.
type Deprecation struct {
	// Message is the value of the deprecationMessage pair, or "" for a tag
	// without one.
	Message string
	// Stated reports whether the tag holds a deprecated or a
	// deprecationMessage pair.
	Stated bool
	// Deprecated reports whether the tag marks the field deprecated, by a
	// deprecated=true pair or a deprecationMessage pair. A deprecated=false
	// pair leaves it false with Stated set, so the tag removes a mark that
	// the doc comment of the field or an embedding field gave.
	Deprecated bool
}

// stateFlag records a deprecated pair.
func (d *Deprecation) stateFlag(deprecated bool) error {
	if !deprecated && d.Message != "" {
		return errDeprecationConflict
	}

	d.Stated = true
	d.Deprecated = d.Deprecated || deprecated

	return nil
}

// stateMessage records a deprecationMessage pair.
func (d *Deprecation) stateMessage(message string) error {
	if message == "" {
		return emptyValueError(keyword.DeprecationMessage)
	}

	if d.Stated && !d.Deprecated {
		return errDeprecationConflict
	}

	d.Message = message
	d.Stated = true
	d.Deprecated = true

	return nil
}

// ParseDeprecation reads the deprecation the jsonschema tag of an embedded
// field states, for the fields that the embed promotes into its parent. It
// reads the deprecated and deprecationMessage pairs alone, under the rules
// [Apply] holds them to, and passes over every other pair, which an
// embedded field that promotes its fields does not apply.
func ParseDeprecation(tag string) (Deprecation, error) {
	var d Deprecation

	directives, _, err := Parse(tag)
	if err != nil {
		return d, err
	}

	seen := map[string]bool{}

	for _, dir := range directives {
		switch dir.Key {
		case keyword.Deprecated, keyword.DeprecationMessage:
		default:
			continue
		}

		if seen[dir.Key] {
			return d, fmt.Errorf("jsonschema tag: key %q is set twice in one tag", dir.Key)
		}

		seen[dir.Key] = true

		if dir.Key == keyword.DeprecationMessage {
			err = d.stateMessage(dir.Value)
		} else {
			var b bool

			b, err = parseFlag(dir.Key, dir.Value)
			if err == nil {
				err = d.stateFlag(b)
			}
		}

		if err != nil {
			return d, err
		}
	}

	return d, nil
}
