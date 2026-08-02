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

// noElementsReason names why a field has no element schema to descend into,
// separating the shapes that genuinely have none from a diveable shape whose
// schema a provider supplied without item sub-schemas.
func noElementsReason(field jsonschema.FieldContext) error {
	form := shapeOf(field).Form

	switch form {
	case tagmodel.FormByteString, tagmodel.FormRawBytes:
		// A []byte encodes as one base64 string, so there are no elements and
		// never will be. Reporting it matches the sequence-wide rules on the
		// same field, which reject rather than dropping a constraint the author
		// wrote; accepting the dive as a no-op here would make the two paths
		// disagree about the same field.
		return errors.New(
			"a []byte field has no item schema to constrain (it encodes as a base64 string)")

	case tagmodel.FormArray, tagmodel.FormObject:
		return errors.New("the schema has no item or value sub-schema")

	default:
		return fmt.Errorf("cannot descend into a %s", form)
	}
}
