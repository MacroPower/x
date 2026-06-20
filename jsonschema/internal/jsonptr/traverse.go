package jsonptr

import (
	"github.com/google/jsonschema-go/jsonschema"
)

// TraverseSchema navigates the schema tree by matching segment names to JSON
// tag names, the typed fast path for JSON-pointer resolution. It returns the
// schema located by following segments from schema, or nil when no typed path
// matches.
//
// Its keyword set parallels the jsonschema package's other per-keyword field
// lists and must stay in sync with them when a sub-schema keyword is added to
// Schema: SubschemaEntries (walk.go), walkSchema, precomputeSchema, and
// checkTypeNames. A keyword missing here is not a correctness bug, because the
// caller (resolveJSONPointer) backstops it by walking the schema's JSON form,
// but the omission silently bypasses this faster typed path.
func TraverseSchema(schema *jsonschema.Schema, segments []string) *jsonschema.Schema {
	if len(segments) == 0 || schema == nil {
		return schema
	}

	seg := segments[0]
	rest := segments[1:]

	// Try map fields first: $defs, definitions, properties, patternProperties,
	// dependentSchemas, DependencySchemas.
	switch seg {
	case "$defs":
		if schema.Defs != nil {
			if len(rest) > 0 {
				if sub, ok := schema.Defs[rest[0]]; ok {
					return TraverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "definitions":
		if schema.Definitions != nil {
			if len(rest) > 0 {
				if sub, ok := schema.Definitions[rest[0]]; ok {
					return TraverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "properties":
		if schema.Properties != nil {
			if len(rest) > 0 {
				if sub, ok := schema.Properties[rest[0]]; ok {
					return TraverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "patternProperties":
		if schema.PatternProperties != nil {
			if len(rest) > 0 {
				if sub, ok := schema.PatternProperties[rest[0]]; ok {
					return TraverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "dependentSchemas":
		if schema.DependentSchemas != nil {
			if len(rest) > 0 {
				if sub, ok := schema.DependentSchemas[rest[0]]; ok {
					return TraverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "dependencies":
		// Draft-07: dependencies is marshaled from DependencySchemas.
		if schema.DependencySchemas != nil {
			if len(rest) > 0 {
				if sub, ok := schema.DependencySchemas[rest[0]]; ok {
					return TraverseSchema(sub, rest[1:])
				}
			}
		}

		return nil

	case "items":
		if schema.Items != nil {
			return TraverseSchema(schema.Items, rest)
		}

		// Array form (Draft-07 items as array): requires an index in rest.
		if len(rest) > 0 && len(schema.ItemsArray) > 0 {
			if idx, ok := ParseArrayIndex(rest[0]); ok && idx < len(schema.ItemsArray) {
				return TraverseSchema(schema.ItemsArray[idx], rest[1:])
			}
		}

		return nil

	case "additionalProperties":
		if schema.AdditionalProperties != nil {
			return TraverseSchema(schema.AdditionalProperties, rest)
		}

		return nil

	case "additionalItems":
		if schema.AdditionalItems != nil {
			return TraverseSchema(schema.AdditionalItems, rest)
		}

		return nil

	case "not":
		if schema.Not != nil {
			return TraverseSchema(schema.Not, rest)
		}

		return nil

	case "if":
		if schema.If != nil {
			return TraverseSchema(schema.If, rest)
		}

		return nil

	case "then":
		if schema.Then != nil {
			return TraverseSchema(schema.Then, rest)
		}

		return nil

	case "else":
		if schema.Else != nil {
			return TraverseSchema(schema.Else, rest)
		}

		return nil

	case "contains":
		if schema.Contains != nil {
			return TraverseSchema(schema.Contains, rest)
		}

		return nil

	case "propertyNames":
		if schema.PropertyNames != nil {
			return TraverseSchema(schema.PropertyNames, rest)
		}

		return nil

	case "unevaluatedProperties":
		if schema.UnevaluatedProperties != nil {
			return TraverseSchema(schema.UnevaluatedProperties, rest)
		}

		return nil

	case "unevaluatedItems":
		if schema.UnevaluatedItems != nil {
			return TraverseSchema(schema.UnevaluatedItems, rest)
		}

		return nil

	case "contentSchema":
		if schema.ContentSchema != nil {
			return TraverseSchema(schema.ContentSchema, rest)
		}

		return nil
	}

	// Slice fields: allOf, anyOf, oneOf, prefixItems.
	idx := -1
	if len(rest) > 0 {
		if n, ok := ParseArrayIndex(rest[0]); ok {
			idx = n
		}
	}

	switch seg {
	case "allOf":
		if idx >= 0 && idx < len(schema.AllOf) {
			return TraverseSchema(schema.AllOf[idx], rest[1:])
		}

		return nil

	case "anyOf":
		if idx >= 0 && idx < len(schema.AnyOf) {
			return TraverseSchema(schema.AnyOf[idx], rest[1:])
		}

		return nil

	case "oneOf":
		if idx >= 0 && idx < len(schema.OneOf) {
			return TraverseSchema(schema.OneOf[idx], rest[1:])
		}

		return nil

	case "prefixItems":
		if idx >= 0 && idx < len(schema.PrefixItems) {
			return TraverseSchema(schema.PrefixItems[idx], rest[1:])
		}

		return nil
	}

	return nil
}
