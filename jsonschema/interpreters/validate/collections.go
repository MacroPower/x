package validate

import (
	"errors"
	"fmt"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// applyDive descends into the element type and applies the remaining parts to
// each element context.
//
// Descending is all this does. The constraints then run through the same
// [applyParts] the field level runs, against element contexts the generator
// supplies, so a rule written under a dive and the same rule written on the
// sequence itself reach the elements by one path and cannot disagree about what
// an element is. What a dive does not do is decide anything about the elements:
// their shapes classify themselves.
func applyDive(remaining []string, field jsonschema.FieldContext) error {
	elems := field.ElementContexts()
	if len(elems) == 0 {
		return fmt.Errorf("validate tag: cannot dive: %w", noElementsReason(field))
	}

	for i := range elems {
		err := applyParts(remaining, elems[i])
		if err != nil {
			return err
		}
	}

	return nil
}

// noElementsReason names why a field has no element schema to descend into.
// The reason itself comes from the shared model, so a dive and a sequence-wide
// rule report the same fact in the same words. Reporting at all -- rather than
// accepting a dive into a shape with no elements as a no-op -- is what keeps the
// two paths agreeing about a field neither can reach into.
func noElementsReason(field jsonschema.FieldContext) error {
	return errors.New(tagmodel.NoElementsReason(shapeOf(field).Form))
}
