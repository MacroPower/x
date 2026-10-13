package jsonschema

import (
	"context"
	"encoding/json/jsontext"
)

// Deprecation is one value of an instance that a schema with
// "deprecated": true applies to, as [Validator.ValidateDeprecations]
// reports it.
type Deprecation struct {
	// Schema is the schema that holds the keyword. Behind a $ref, it is the
	// schema that the $ref reaches. It belongs to the [Validator], as
	// [Validator.Schema] does, so treat it as read-only.
	Schema *Schema

	// InstancePath is the JSON Pointer to the value.
	InstancePath jsontext.Pointer

	// SchemaPath is the JSON Pointer to the schema that holds the keyword,
	// along the path the walk took, through each $ref, as
	// [ValidationError.SchemaPath] reads. It names the schema, not its
	// deprecated keyword.
	SchemaPath jsontext.Pointer

	// The typed form of InstancePath; see [Deprecation.InstanceSegments].
	segments []Segment

	// The typed form of SchemaPath; see [Deprecation.SchemaSegments].
	schemaSegs []Segment

	// The record marks the name of the member at InstancePath; see
	// [Deprecation.TargetsKey].
	targetsKey bool
}

// InstanceSegments returns InstancePath as typed segments, one [Segment]
// per reference token, outermost first, as
// [ValidationError.InstanceSegments] does.
func (d Deprecation) InstanceSegments() []Segment {
	return d.segments
}

// SchemaSegments returns SchemaPath as typed segments, one [Segment] per
// reference token, outermost first, as [ValidationError.SchemaSegments]
// does.
func (d Deprecation) SchemaSegments() []Segment {
	return d.schemaSegs
}

// TargetsKey reports whether the keyword marks the name of the member at
// InstancePath rather than its value. It is true when Schema is the schema
// that properties, patternProperties, additionalProperties, or
// unevaluatedProperties gives that member, and for every record under
// propertyNames. A schema that a $ref reaches from such a schema marks the
// value.
func (d Deprecation) TargetsKey() bool {
	return d.targetsKey
}

// ValidateDeprecations validates instance as [Validator.Validate] does, and
// returns each [Deprecation] that applies to instance, whether instance
// passes or fails. The error is the one Validate returns for instance.
//
// Each evaluation of a schema with "deprecated": true against a value adds
// one record for that value, whatever other keywords the schema holds. The
// keyword asserts nothing, so it never fails a value. Records come in the
// order of the walk, so a schema that two paths reach at one value gives one
// record for each path. A cycle that the walk cuts gives no record. Under
// [Draft7], a schema with a $ref gives none, because that draft ignores the
// siblings of $ref. Every set of vocabularies reports the keyword.
//
// A record drops only where the schema chooses a path that the value does
// not take:
//
//   - A branch of anyOf or oneOf gives its records only when the value
//     passes that branch. A oneOf that fails because several branches pass
//     still gives the records of each one.
//   - The if keyword gives its records only when the value passes it. The
//     then or else keyword that if picks gives its records whether the value
//     passes it or not, and the walk never reaches the other.
//   - The contains keyword gives the records of the items that match it.
//   - The not keyword gives none, because the value must fail its schema.
//
// Every other applicator passes up the records of its subschemas whether
// they pass or fail. A failing allOf or $ref thus keeps its records, though
// it drops the annotations that unevaluatedProperties and unevaluatedItems
// read. Those keywords walk a subschema that holds "deprecated": true for
// each member or item they reach, even when the subschema asserts nothing.
//
// The context reaches the [RefResolver] for remote refs, as it does for
// Validate.
func (c *Validator) ValidateDeprecations(ctx context.Context, instance any) ([]Deprecation, error) {
	value, err := normalizeAndCheck(instance)
	if err != nil {
		return nil, err
	}

	log := &deprecationLog{}

	err = c.validateNormalized(ctx, value, log)

	return log.records, err
}

// deprecationLog collects the [Deprecation] records of one validation run.
// The walk is depth-first and sequential, so an applicator that drops the
// records of a subschema takes the length of the log before it walks the
// subschema and cuts the log back to it after.
type deprecationLog struct {
	records []Deprecation
	// The number of propertyNames walks in progress. A record under one
	// marks the name of the member.
	names int
}

// keyApplicators names the keywords whose subschema judges the value of an
// object member, so a deprecation on that subschema marks the member's name.
var keyApplicators = map[string]bool{
	KeywordProperties:            true,
	KeywordPatternProperties:     true,
	KeywordAdditionalProperties:  true,
	KeywordUnevaluatedProperties: true,
}

// recordDeprecation adds the record of schema, which holds "deprecated":
// true, at the instance and schema locations the walk is at. A run with no
// log records nothing.
func (v *validator) recordDeprecation(schema *Schema, instancePath instanceLocation, schemaPath schemaLocation) {
	log := v.deprecations
	if log == nil {
		return
	}

	log.records = append(log.records, Deprecation{
		Schema:       schema,
		segments:     cloneSegments(instancePath.segs),
		schemaSegs:   cloneSegments(schemaPath.segs),
		InstancePath: instancePath.pointer(),
		SchemaPath:   schemaPath.pointer(),
		targetsKey:   log.names > 0 || keyApplicators[schemaPath.keyword],
	})
}

// markDeprecations returns the length of the run's log, for
// [validator.dropDeprecations] to cut it back to. A run with no log returns
// 0.
func (v *validator) markDeprecations() int {
	if v.deprecations == nil {
		return 0
	}

	return len(v.deprecations.records)
}

// dropDeprecations cuts the run's log back to the length mark, dropping the
// records of a subschema the value did not take.
func (v *validator) dropDeprecations(mark int) {
	if v.deprecations == nil {
		return
	}

	clear(v.deprecations.records[mark:])

	v.deprecations.records = v.deprecations.records[:mark]
}

// collectsDeprecations reports whether the run collects records and s holds
// "deprecated": true, so an applicator that skips a subschema that asserts
// nothing walks it still.
func (v *validator) collectsDeprecations(s *Schema) bool {
	return v.deprecations != nil && s.Deprecated
}
