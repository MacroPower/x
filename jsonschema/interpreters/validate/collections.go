package validate

import (
	"fmt"
	"reflect"
	"strconv"

	"go.jacobcolvin.com/x/jsonschema"
)

// applyCollectionMinConstraint applies min/gte or gt to a collection schema.
func applyCollectionMinConstraint(s *jsonschema.Schema, value string, baseType reflect.Type, exclusive bool) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("validate tag: invalid number %q: %w", value, err)
	}
	// Gt=N means minItems N+1, clamped to a non-negative bound as JSON Schema
	// requires.
	n = clampNonNegative(inclusiveLowerBound(n, exclusive))
	// Rules in a validate tag are ANDed, so overlapping minimum floors intersect
	// to their maximum. The floor only ever rises: a weaker (lower) min never
	// lowers a stronger floor set by another part of the tag, regardless of order.
	if isMapKind(baseType) {
		if s.MinProperties == nil || n > *s.MinProperties {
			s.MinProperties = jsonschema.Ptr(n)
		}
	} else {
		if s.MinItems == nil || n > *s.MinItems {
			s.MinItems = jsonschema.Ptr(n)
		}
	}

	return nil
}

// applyCollectionMaxConstraint applies max/lte or lt to a collection schema.
func applyCollectionMaxConstraint(s *jsonschema.Schema, value string, baseType reflect.Type, exclusive bool) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("validate tag: invalid number %q: %w", value, err)
	}
	// Lt=N means maxItems N-1, clamped to a non-negative bound as JSON Schema
	// requires (so lt=0 collapses to 0).
	n = clampNonNegative(inclusiveUpperBound(n, exclusive))
	// Rules in a validate tag are ANDed, so overlapping maximum ceilings
	// intersect to their minimum. The ceiling only ever falls: a weaker (higher)
	// max never raises a stronger ceiling set by another part of the tag.
	if isMapKind(baseType) {
		if s.MaxProperties == nil || n < *s.MaxProperties {
			s.MaxProperties = jsonschema.Ptr(n)
		}
	} else {
		if s.MaxItems == nil || n < *s.MaxItems {
			s.MaxItems = jsonschema.Ptr(n)
		}
	}

	return nil
}

// applyCollectionLenConstraint applies len=N to a collection schema.
func applyCollectionLenConstraint(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("validate tag: invalid number %q: %w", value, err)
	}
	// Min/maxItems and min/maxProperties MUST be non-negative per JSON Schema;
	// a negative length collapses to 0.
	n = clampNonNegative(n)
	if isMapKind(baseType) {
		s.MinProperties = jsonschema.Ptr(n)
		s.MaxProperties = jsonschema.Ptr(n)
	} else {
		s.MinItems = jsonschema.Ptr(n)
		s.MaxItems = jsonschema.Ptr(n)
	}

	return nil
}

// applyCollectionNe applies ne=N to a collection schema, forbidding the length
// N. The exclusion is expressed as a not subschema pinning the length so a
// collection of exactly N elements (or entries, for a map) is rejected.
func applyCollectionNe(s *jsonschema.Schema, value string, baseType reflect.Type) error {
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
		forbidden.MinProperties = jsonschema.Ptr(n)
		forbidden.MaxProperties = jsonschema.Ptr(n)
	} else {
		forbidden.MinItems = jsonschema.Ptr(n)
		forbidden.MaxItems = jsonschema.Ptr(n)
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

		return applyParts(remaining, s.AdditionalProperties, nil, "", ft.Elem(), true)

	default:
		return fmt.Errorf("validate tag: cannot dive into non-collection type %s", ft.Kind())
	}
}

// diveIntoSequence applies the remaining dive constraints to the element schema
// of a slice or fixed array. The generator represents element schemas
// differently depending on the field kind: plain slices use Items, fixed arrays
// use prefixItems (Draft 2020-12) or the items-as-array form (Draft-07), and
// []byte becomes a single base64 string with no element schema at all. Each of
// these is dived into so a dive tag on those kinds does not abort generation.
func diveIntoSequence(remaining []string, s *jsonschema.Schema, elem reflect.Type) error {
	switch {
	case s.Items != nil:
		return applyParts(remaining, s.Items, nil, "", elem, true)

	case len(s.PrefixItems) > 0:
		for _, item := range s.PrefixItems {
			err := applyParts(remaining, item, nil, "", elem, true)
			if err != nil {
				return err
			}
		}

		return nil

	case len(s.ItemsArray) > 0:
		for _, item := range s.ItemsArray {
			err := applyParts(remaining, item, nil, "", elem, true)
			if err != nil {
				return err
			}
		}

		return nil

	case s.ContentEncoding == base64Encoding:
		// A []byte field marshals to a single base64 string, so there is no
		// per-element schema to constrain. The dive has no representable target;
		// accept it as a no-op rather than aborting generation.
		return nil

	default:
		return fmt.Errorf("validate tag: cannot dive: array schema has no items")
	}
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
