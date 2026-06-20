package validate

import (
	"errors"
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

// collectionBoundFields returns the schema's size-bound field pointers for the
// collection's base type: the min/maxProperties pair for a map, otherwise the
// min/maxItems pair for a slice or array. The first result is the floor field,
// the second the ceiling field.
func collectionBoundFields(s *jsonschema.Schema, baseType reflect.Type) (**int, **int) {
	if isMapKind(baseType) {
		return &s.MinProperties, &s.MaxProperties
	}

	return &s.MinItems, &s.MaxItems
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

// applyCollectionMinConstraint applies min/gte or gt to a collection schema by
// raising its size floor (minItems or minProperties).
func applyCollectionMinConstraint(s *jsonschema.Schema, value string, baseType reflect.Type, exclusive bool) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	minField, _ := collectionBoundFields(s, baseType)

	return applyMinBound(minField, value, exclusive)
}

// applyCollectionMaxConstraint applies max/lte or lt to a collection schema by
// lowering its size ceiling (maxItems or maxProperties).
func applyCollectionMaxConstraint(s *jsonschema.Schema, value string, baseType reflect.Type, exclusive bool) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	minField, maxField := collectionBoundFields(s, baseType)

	return applyMaxBound(minField, maxField, value, exclusive)
}

// applyCollectionLenConstraint applies len=N to a collection schema by pinning
// both size bounds to the intersected value.
func applyCollectionLenConstraint(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	if isByteSliceField(baseType) {
		return errByteSliceLengthConstraint
	}

	minField, maxField := collectionBoundFields(s, baseType)

	return applyLenBound(minField, maxField, value)
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

// applyDive descends into the element type for slice/array/map and applies
// remaining parts to the items/additionalProperties sub-schema.
func applyDive(remaining []string, s *jsonschema.Schema, fieldType reflect.Type) error {
	// Follow pointers.
	ft := fieldType
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}

	switch ft.Kind() {
	case reflect.Slice, reflect.Array:
		return diveIntoSequence(remaining, s, ft.Elem())

	case reflect.Map:
		if s.AdditionalProperties == nil {
			return fmt.Errorf("validate tag: cannot dive: map schema has no additionalProperties")
		}

		err := applyParts(remaining, s.AdditionalProperties, nil, "", ft.Elem(), true)
		if err != nil {
			return err
		}

		err = relocateNullableValueConstraint(s.AdditionalProperties)
		if err != nil {
			return err
		}

		dropElementBoundsForConstEnum(s.AdditionalProperties)

		return nil

	default:
		return fmt.Errorf("validate tag: cannot dive into non-collection type %s", ft.Kind())
	}
}

// diveIntoSequence applies the remaining dive constraints to the element schema
// of a slice or fixed array. Each of the element-schema shapes
// (see [schemashape.ItemSchemas]) is dived into so a dive tag on those kinds
// does not abort generation.
func diveIntoSequence(remaining []string, s *jsonschema.Schema, elem reflect.Type) error {
	if items := schemashape.ItemSchemas(s); len(items) > 0 {
		for _, item := range items {
			err := applyParts(remaining, item, nil, "", elem, true)
			if err != nil {
				return err
			}

			err = relocateNullableValueConstraint(item)
			if err != nil {
				return err
			}

			dropElementBoundsForConstEnum(item)
		}

		return nil
	}

	if s.ContentEncoding == base64Encoding {
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
