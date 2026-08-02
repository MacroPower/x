package validate

import (
	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// base64Encoding is the content validator tag and the contentEncoding keyword
// value for base64-encoded string content.
const base64Encoding = "base64"

// validatorRule is one row of this dialect's key table: the shared rule, plus
// the dialect's own explanation for a key whose declared arity an author is
// likely to get wrong.
//
// The note lives on the row rather than in a second map keyed by the same
// strings, so it cannot drift from the rule it explains -- the same reason the
// shared model puts a dialect's divergence on its key row rather than beside it.
type validatorRule struct {
	// The paramNote field is a format verb taking the offending value. It
	// replaces the generic arity message so the error says what is actually
	// unrepresentable, not merely that the parameter was unexpected.
	paramNote string
	tagmodel.KeyRule
}

// validatorKeys is this dialect's whole vocabulary: every validator that
// expresses a value constraint, mapped to the shared operation it names and the
// way go-playground spells its parameter.
//
// Declaring the spelling here is what makes a key that ignores its parameter a
// table error rather than an unwritten assumption. A ParamNone row takes no
// parameter and supplies its Implied value instead, so unique means
// uniqueItems=true and email means format=email; a ParamRequired row must carry
// one; a ParamList row carries several, tokenized the way this dialect spells a
// list. A row leaves Axis unset because go-playground names rules, not keywords:
// min is a length on a string and a count on a slice, and the field's shape
// decides which. SizedOp records the one place the dialect spells a size rule
// with a value rule's key: eq=N on a slice is len(slice)==N.
var validatorKeys = map[string]validatorRule{
	"required": {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpNonZero, Param: tagmodel.ParamNone}},

	"min": {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpFloorIncl, Param: tagmodel.ParamRequired}},
	"gte": {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpFloorIncl, Param: tagmodel.ParamRequired}},
	"gt":  {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpFloorExcl, Param: tagmodel.ParamRequired}},
	"max": {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpCeilIncl, Param: tagmodel.ParamRequired}},
	"lte": {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpCeilIncl, Param: tagmodel.ParamRequired}},
	"lt":  {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpCeilExcl, Param: tagmodel.ParamRequired}},
	"len": {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpExactSize, Param: tagmodel.ParamRequired}},

	"eq": {KeyRule: tagmodel.KeyRule{
		Op: tagmodel.OpEqual, SizedOp: tagmodel.OpExactSize, Param: tagmodel.ParamRequired,
	}},
	"ne": {KeyRule: tagmodel.KeyRule{
		Op: tagmodel.OpNotEqual, SizedOp: tagmodel.OpForbidSize, Param: tagmodel.ParamRequired,
	}},

	"oneof": {KeyRule: tagmodel.KeyRule{Op: tagmodel.OpOneOf, Param: tagmodel.ParamList, Split: splitOneOfValues}},

	// The bare form asserts uniqueness of whole elements. The unique=<field>
	// form asserts it of one named field across struct elements, which
	// uniqueItems cannot express, so declaring this row ParamNone is what turns
	// that parameter into an error instead of a silently weakened constraint.
	"unique": {
		KeyRule:   tagmodel.KeyRule{Op: tagmodel.OpUnique, Param: tagmodel.ParamNone, Implied: "true"},
		paramNote: "unique=%s has no JSON Schema equivalent (uniqueItems compares whole elements)",
	},

	"email":    formatKey("email"),
	"url":      formatKey("uri"),
	"uri":      formatKey("uri-reference"),
	"uuid":     formatKey("uuid"),
	"ipv4":     formatKey("ipv4"),
	"ipv6":     formatKey("ipv6"),
	"hostname": formatKey("hostname"),

	"alpha":    patternKey(`^[a-zA-Z]+$`),
	"alphanum": patternKey(`^[a-zA-Z0-9]+$`),
	"numeric":  patternKey(`^[-+]?[0-9]+(?:\.[0-9]+)?$`),
	"number":   patternKey(`^[0-9]+$`),
	"ascii":    patternKey(`^[\x00-\x7F]*$`),

	"json": {KeyRule: tagmodel.KeyRule{
		Op: tagmodel.OpContentMediaType, Param: tagmodel.ParamNone, Implied: "application/json",
	}},
	base64Encoding: {KeyRule: tagmodel.KeyRule{
		Op: tagmodel.OpContentEncoding, Param: tagmodel.ParamNone, Implied: base64Encoding,
	}},
}

// formatKey is a bare validator naming a format value, which the row supplies
// since the tag itself carries none.
func formatKey(format string) validatorRule {
	return validatorRule{KeyRule: tagmodel.KeyRule{
		Op: tagmodel.OpFormat, Param: tagmodel.ParamNone, Implied: format,
	}}
}

// patternKey is a bare validator naming a regular expression, spelled the same
// way as a format key.
func patternKey(pattern string) validatorRule {
	return validatorRule{KeyRule: tagmodel.KeyRule{
		Op: tagmodel.OpPattern, Param: tagmodel.ParamNone, Implied: pattern,
	}}
}

// isControlTag reports whether a key is a go-playground/validator control tag
// that governs when validation runs rather than expressing a value constraint.
// These have no JSON Schema representation and are skipped.
func isControlTag(key string) bool {
	switch key {
	case "omitempty", "omitnil", "omitzero", "structonly", "nostructlevel",
		"isdefault":
		return true
	}

	return false
}

// isCrossFieldValidator reports whether a key is a cross-field validator that
// should be silently ignored.
func isCrossFieldValidator(key string) bool {
	switch key {
	case "eqfield", "nefield", "gtfield", "gtefield", "ltfield", "ltefield",
		"eqcsfield", "necsfield", "gtcsfield", "gtecsfield", "ltcsfield", "ltecsfield",
		"required_if", "required_unless", "required_with", "required_with_all",
		"required_without", "required_without_all", "excluded_if", "excluded_unless",
		"excluded_with", "excluded_with_all", "excluded_without", "excluded_without_all",
		"skip_unless", "fieldcontains", "fieldexcludes":
		return true
	}

	return false
}

// shapeOf classifies a field or element the way the shared model does, so this
// package can resolve a key's operation against the shape before applying it.
func shapeOf(field jsonschema.FieldContext) tagmodel.Shape {
	return tagmodel.ShapeOf(field.Type, field.Base)
}
