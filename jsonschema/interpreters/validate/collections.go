package validate

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"go.jacobcolvin.com/x/jsonschema"
)

// errByteSliceLengthConstraint reports a length, size, or uniqueness validator
// applied to a []byte field. A []byte marshals to a single base64 string, so
// the array keywords such a validator would set (minItems, maxItems,
// uniqueItems) have no effect on the string instance. Rejecting the tag
// surfaces the unrepresentable constraint rather than silently dropping it,
// matching how oneof on a []byte field is handled.
var errByteSliceLengthConstraint = errors.New(
	"validate tag: a length or uniqueness constraint on a []byte field has no array length to constrain (it encodes as a base64 string)",
)

// isByteSliceField reports whether baseType is a []byte, which marshals to a
// single base64 string rather than a JSON array.
func isByteSliceField(baseType reflect.Type) bool {
	return baseType.Kind() == reflect.Slice && baseType.Elem().Kind() == reflect.Uint8
}

// applyCollectionMinConstraint applies min/gte or gt to a collection field by
// raising its size floor (minItems or minProperties) through the shared
// constraints facade, which selects the count keyword from the field's kind and
// intersects the bound against the effective floor.
func applyCollectionMinConstraint(
	field jsonschema.FieldContext,
	value string,
	baseType reflect.Type,
	exclusive bool,
) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	rule := jsonschema.LenMin
	if exclusive {
		rule = jsonschema.LenGt
	}

	err := field.Constraints().AddCountBound(rule, value)
	if err != nil {
		return fmt.Errorf("validate tag: min: %w", err)
	}

	return nil
}

// applyCollectionMaxConstraint applies max/lte or lt to a collection field by
// lowering its size ceiling (maxItems or maxProperties) through the facade.
func applyCollectionMaxConstraint(
	field jsonschema.FieldContext,
	value string,
	baseType reflect.Type,
	exclusive bool,
) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	rule := jsonschema.LenMax
	if exclusive {
		rule = jsonschema.LenLt
	}

	err := field.Constraints().AddCountBound(rule, value)
	if err != nil {
		return fmt.Errorf("validate tag: max: %w", err)
	}

	return nil
}

// applyCollectionLenConstraint applies len=N to a collection field by pinning
// both size bounds through the facade to the intersected value.
func applyCollectionLenConstraint(field jsonschema.FieldContext, value string, baseType reflect.Type) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	err := field.Constraints().AddCountBound(jsonschema.LenExact, value)
	if err != nil {
		return fmt.Errorf("validate tag: len: %w", err)
	}

	return nil
}

// applyCollectionNe applies ne=N to a collection field, forbidding the length N.
// The exclusion is expressed as a not subschema pinning the length so a
// collection of exactly N elements (or entries, for a map) is rejected; it rides
// the shared facade ForbidSchema so it composes with any value already forbidden.
func applyCollectionNe(field jsonschema.FieldContext, value string, baseType reflect.Type) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("validate tag: invalid number %q: %w", value, err)
	}

	// A negative length can never occur, so ne=<negative> excludes nothing.
	if n < 0 {
		return nil
	}

	forbidden := &jsonschema.Schema{}
	if isMapKind(baseType) {
		forbidden.MinProperties = new(n)
		forbidden.MaxProperties = new(n)
	} else {
		forbidden.MinItems = new(n)
		forbidden.MaxItems = new(n)
	}

	field.Constraints().ForbidSchema(forbidden)

	return nil
}

// applyDive descends into the element type for slice/array/map and applies the
// remaining parts to each element context.
func applyDive(remaining []string, field jsonschema.FieldContext) error {
	// Follow pointers.
	ft := field.Type
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}

	switch ft.Kind() {
	case reflect.Slice, reflect.Array:
		return diveIntoSequence(remaining, field)

	case reflect.Map:
		elems := field.ElementContexts()
		if len(elems) == 0 {
			return fmt.Errorf("validate tag: cannot dive: map schema has no additionalProperties")
		}

		value := elems[0]

		err := applyParts(remaining, value, true)
		if err != nil {
			return err
		}

		return finishElement(value)

	default:
		return fmt.Errorf("validate tag: cannot dive into non-collection type %s", ft.Kind())
	}
}

// diveIntoSequence applies the remaining dive constraints to each element
// context of a slice or fixed array (its single item, or every array position),
// so a dive tag on those kinds does not abort generation. A []byte field
// marshals to a single base64 string with no per-element schema, so its dive is
// a no-op rather than a generation error.
func diveIntoSequence(remaining []string, field jsonschema.FieldContext) error {
	if elems := field.ElementContexts(); len(elems) > 0 {
		for i := range elems {
			elem := elems[i]

			err := applyParts(remaining, elem, true)
			if err != nil {
				return err
			}

			err = finishElement(elem)
			if err != nil {
				return err
			}
		}

		return nil
	}

	if field.Base != nil && field.Base.ContentEncoding == base64Encoding {
		// A []byte field marshals to a single base64 string, so there is no
		// per-element schema to constrain. The dive has no representable target;
		// accept it as a no-op rather than aborting generation.
		return nil
	}

	return fmt.Errorf("validate tag: cannot dive: array schema has no items")
}

// isCollectionKind reports whether the type is a slice, array, or map kind.
func isCollectionKind(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return true
	default:
		return false
	}
}

// isSequenceKind reports whether the type is a slice or array kind. Maps are
// excluded: keywords such as uniqueItems apply only to JSON arrays, not objects.
func isSequenceKind(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return true
	default:
		return false
	}
}

// isMapKind reports whether the type is a map kind.
func isMapKind(t reflect.Type) bool {
	return t.Kind() == reflect.Map
}
