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
// their shapes classify themselves. The container's form travels with the
// descent, since a keys block right after the dive is legal only under a map.
func applyDive(remaining []string, field jsonschema.FieldContext) error {
	form := shapeOf(field).Form

	elems := field.ElementContexts()
	if len(elems) == 0 {
		// The shape decides whether there is anything to descend into, as it
		// does for go-playground, which panics on a dive over anything but a
		// slice, array, or map. A caller-built context supplies no element
		// canvases even for a collection, so a trailing dive there descends
		// into nothing and applies nothing, while a dive carrying constraints
		// it cannot place is still an error rather than a silent drop.
		if (form == tagmodel.FormArray || form == tagmodel.FormObject) && !hasConstraint(remaining) {
			return nil
		}

		return fmt.Errorf("validate tag: cannot dive: %w", noElementsReason(field))
	}

	for i := range elems {
		err := applyParts(remaining, elems[i], true, form)
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
