package validate

import (
	"errors"
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"
)

// collectionBounds returns the collection's size-bound canvas write targets (the
// floor and ceiling field pointers) and their effective floor and ceiling
// values, in that order: the min/maxProperties pair for a map, otherwise the
// min/maxItems pair for a slice or array. The effective values coalesce the
// canvas with the type-derived base so a tag bound never weakens the type's own.
func collectionBounds(
	field jsonschema.FieldContext, baseType reflect.Type,
) (**int, **int, *int, *int) {
	if isMapKind(baseType) {
		return &field.Schema.MinProperties, &field.Schema.MaxProperties,
			field.EffectiveMinProperties(), field.EffectiveMaxProperties()
	}

	return &field.Schema.MinItems, &field.Schema.MaxItems,
		field.EffectiveMinItems(), field.EffectiveMaxItems()
}

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
// raising its size floor (minItems or minProperties) on the canvas.
func applyCollectionMinConstraint(
	field jsonschema.FieldContext,
	value string,
	baseType reflect.Type,
	exclusive bool,
) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	minField, _, effMin, _ := collectionBounds(field, baseType)

	return applyMinBound(minField, effMin, value, exclusive)
}

// applyCollectionMaxConstraint applies max/lte or lt to a collection field by
// lowering its size ceiling (maxItems or maxProperties) on the canvas.
func applyCollectionMaxConstraint(
	field jsonschema.FieldContext,
	value string,
	baseType reflect.Type,
	exclusive bool,
) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	minField, maxField, effMin, effMax := collectionBounds(field, baseType)

	return applyMaxBound(minField, maxField, effMin, effMax, value, exclusive)
}

// applyCollectionLenConstraint applies len=N to a collection field by pinning
// both size bounds on the canvas to the intersected value.
func applyCollectionLenConstraint(field jsonschema.FieldContext, value string, baseType reflect.Type) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	minField, maxField, effMin, effMax := collectionBounds(field, baseType)

	return applyLenBound(minField, maxField, effMin, effMax, value)
}

// applyCollectionNe applies ne=N to a collection schema, forbidding the length
// N. The exclusion is expressed as a not subschema pinning the length so a
// collection of exactly N elements (or entries, for a map) is rejected.
func applyCollectionNe(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	n, err := parseBoundValue(value)
	if err != nil {
		return err
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

	if s.Not == nil {
		s.Not = forbidden

		return nil
	}

	// A length exclusion is a min/max range rather than a single value, so it
	// cannot ride on forbidValue's not.const/not.enum accumulation. Instead move
	// any existing not under allOf and add a separate not for this length so both
	// apply conjunctively.
	s.AllOf = append(s.AllOf,
		&jsonschema.Schema{Not: s.Not},
		&jsonschema.Schema{Not: forbidden},
	)
	s.Not = nil

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
