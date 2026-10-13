package tagparse

import (
	"fmt"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// errDeprecationConflict reports a deprecationMessage pair beside a
// deprecated=false pair, two rules on the field that can never both hold. It
// wraps [tagmodel.ErrConflict], which the main package exports as
// ErrConstraintConflict.
var errDeprecationConflict = fmt.Errorf(
	"jsonschema tag: key %q deprecates the field that deprecated=false keeps current: %w",
	keyword.DeprecationMessage, tagmodel.ErrConflict,
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

// state records one deprecated or deprecationMessage pair.
func (d *Deprecation) state(key, value string) error {
	if key == keyword.DeprecationMessage {
		return d.stateMessage(value)
	}

	b, err := parseFlag(key, value)
	if err != nil {
		return err
	}

	return d.stateFlag(b)
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

// applyDeprecation applies one deprecated or deprecationMessage pair. A key
// may appear once in a tag, as every overwriting key may, and the pair joins
// the deprecation the tag states. The canvas, when the state has one, takes
// the mark that the deprecation reads.
func (s *applyState) applyDeprecation(key, value string) error {
	err := s.checkRepeat(key)
	if err != nil {
		return err
	}

	err = s.deprecation.state(key, value)
	if err != nil {
		return err
	}

	if s.canvas != nil {
		s.canvas.Deprecated = s.deprecation.Deprecated
	}

	return nil
}

// ParseDeprecation reads the deprecation the jsonschema tag of an embedded
// field states, for the fields that the embed promotes into its parent. It
// reads the deprecated and deprecationMessage pairs alone, under the rules
// [Apply] holds them to, and passes over every other pair, which an
// embedded field that promotes its fields does not apply.
func ParseDeprecation(tag string) (Deprecation, error) {
	directives, _, err := Parse(tag)
	if err != nil {
		return Deprecation{}, err
	}

	s := &applyState{seen: map[string]bool{}}

	for _, dir := range directives {
		if dir.Key != keyword.Deprecated && dir.Key != keyword.DeprecationMessage {
			continue
		}

		err := s.applyDeprecation(dir.Key, dir.Value)
		if err != nil {
			return Deprecation{}, err
		}
	}

	return s.deprecation, nil
}
