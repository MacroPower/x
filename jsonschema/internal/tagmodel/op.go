package tagmodel

import (
	"errors"
	"fmt"
)

var (
	// ErrConflict reports two rules on one target that can never both hold: a
	// second const pinned to a different value, a second enum, or a second
	// authored value for a first-wins string keyword. The main package
	// re-exports it, so a conflict raised here is recognizable through the
	// public sentinel and through each dialect's own derived one.
	ErrConflict = errors.New("conflicting value constraints")

	// ErrUnsupported reports an operation the target's shape cannot carry. A
	// front-end wraps it with its own dialect prefix; the message body comes
	// from the rejecting cell's written reason, so one rejection is one string
	// in one place.
	ErrUnsupported = errors.New("constraint not supported for this shape")

	// The opNames table labels each operation for the matrix dump, the panic messages,
	// and the rejection text.
	opNames = [opCount]string{
		OpUnset:            "unset",
		OpFloorIncl:        "inclusive floor",
		OpFloorExcl:        "exclusive floor",
		OpCeilIncl:         "inclusive ceiling",
		OpCeilExcl:         "exclusive ceiling",
		OpExactSize:        "exact size",
		OpForbidSize:       "forbidden size",
		OpEqual:            "pinned value",
		OpNotEqual:         "forbidden value",
		OpOneOf:            "enumerated values",
		OpNonZero:          "non-zero assertion",
		OpUnique:           "element uniqueness",
		OpMultipleOf:       "divisor",
		OpFormat:           "format",
		OpPattern:          "pattern",
		OpContentEncoding:  "content encoding",
		OpContentMediaType: "content media type",
	}

	// The axisNames table labels each axis for the compatibility table's rejection text.
	axisNames = [axisCount]string{
		AxisAuto:       "auto",
		AxisNumeric:    "numeric",
		AxisLength:     "length",
		AxisItems:      "item count",
		AxisProperties: "property count",
	}
)

// Op is one constraint operation, the vocabulary both tag dialects spell in
// their own words. It is the row index of the dispatch matrix.
type Op uint8

const (
	// OpUnset is the zero value and never names an operation.
	OpUnset Op = iota
	// OpFloorIncl is an inclusive floor: min, gte, minimum, minLength, minItems,
	// minProperties.
	OpFloorIncl
	// OpFloorExcl is an exclusive floor: gt, exclusiveMinimum.
	OpFloorExcl
	// OpCeilIncl is an inclusive ceiling: max, lte, maximum, maxLength,
	// maxItems, maxProperties.
	OpCeilIncl
	// OpCeilExcl is an exclusive ceiling: lt, exclusiveMaximum.
	OpCeilExcl
	// OpExactSize pins a size to N: len, and eq on a shape whose instance has a
	// size rather than a value to pin.
	OpExactSize
	// OpForbidSize excludes one size: ne on a sized shape, where the tag forbids
	// a length or entry count rather than a value.
	OpForbidSize
	// OpEqual pins the value: eq, const.
	OpEqual
	// OpNotEqual forbids one value: ne.
	OpNotEqual
	// OpOneOf enumerates the allowed values: oneof, enum.
	OpOneOf
	// OpNonZero is the non-zero assertion required makes, whose shape depends
	// entirely on what "empty" means for the instance.
	OpNonZero
	// OpUnique asserts distinct array elements: unique, uniqueItems.
	OpUnique
	// OpMultipleOf sets a divisor.
	OpMultipleOf
	// OpFormat sets the format keyword.
	OpFormat
	// OpPattern sets the pattern keyword.
	OpPattern
	// OpContentEncoding sets the contentEncoding keyword.
	OpContentEncoding
	// OpContentMediaType sets the contentMediaType keyword.
	OpContentMediaType
	// The opCount constant bounds the matrix rows. Adding an operation above grows every
	// column, which the init-time totality walk then reports as unfilled.
	opCount
)

// String returns the operation's label.
func (op Op) String() string {
	if op >= opCount {
		return fmt.Sprintf("Op(%d)", uint8(op))
	}

	return opNames[op]
}

// IsStringKeyword reports whether the operation sets one of the string keywords
// (format, pattern, and the content pair).
func (op Op) IsStringKeyword() bool {
	switch op {
	case OpFormat, OpPattern, OpContentEncoding, OpContentMediaType:
		return true
	default:
		return false
	}
}

// Overwrites reports whether the operation's applier replaces a value already
// in force rather than composing with it: the string keywords and the divisor.
// A bound intersects and a const or enum conflicts on its own, so those
// compose. A dialect that has to notice a repeated value asks here rather than
// naming the set itself and going stale.
func (op Op) Overwrites() bool {
	return op.IsStringKeyword() || op == OpMultipleOf
}

// Axis pins the keyword family a bound targets. [AxisAuto] lets the shape
// choose, which is what a dialect naming a rule rather than a keyword means:
// validate's min is a length on a string and a count on a slice. A dialect that
// names the keyword directly (minItems) pins the axis instead, so a shape with
// no such keyword is an error rather than inert output.
type Axis uint8

const (
	// AxisAuto defers to the shape's natural axis.
	AxisAuto Axis = iota
	// AxisNumeric targets minimum and its siblings.
	AxisNumeric
	// AxisLength targets minLength and maxLength.
	AxisLength
	// AxisItems targets minItems and maxItems.
	AxisItems
	// AxisProperties targets minProperties and maxProperties.
	AxisProperties
	// The axisCount constant bounds the axis-compatibility table.
	axisCount
)

// String returns the axis label.
func (a Axis) String() string {
	if a >= axisCount {
		return fmt.Sprintf("Axis(%d)", uint8(a))
	}

	return axisNames[a]
}

// ParamMode declares how a key spells its parameter. It lives on the key table
// rather than on [Op] because the same operation is spelled with and without a
// parameter across dialects: validate's unique takes none while the jsonschema
// tag's uniqueItems=true takes a boolean, and validate's email takes none while
// format=email carries its value.
type ParamMode uint8

const (
	// ParamNone is a bare key. It supplies [KeyRule.Implied] as its value, so
	// unique behaves as uniqueItems=true and email as format=email.
	ParamNone ParamMode = iota
	// ParamRequired is a key carrying exactly one non-empty value.
	ParamRequired
	// ParamList is a key carrying one or more values, tokenized by the row's
	// own [KeyRule.Split].
	ParamList
)

// KeyRule is one row of a dialect's key table: the operation a key names, the
// axis it pins, and how the dialect spells its parameter. Declaring arity here
// rather than deriving it in an apply switch is what makes a key that ignores
// its parameter a table error instead of an unwritten assumption.
type KeyRule struct {
	// Split tokenizes a ParamList value. It is required for ParamList and unused
	// otherwise: the dialects spell a list differently (a quote-aware whitespace
	// split, a pipe split), so the spelling belongs with the key.
	Split func(string) []string
	// Implied is the value a ParamNone key supplies: "true" for unique, "email"
	// for the email format tag.
	Implied string
	// Op is the operation the key names.
	Op Op
	// SizedOp replaces Op on a shape whose instance carries a size rather than a
	// value, where a dialect spells a size rule with the same key it spells a
	// value rule: go-playground's eq=N on a slice means len(slice)==N, while the
	// jsonschema tag's const= is always a value. OpUnset keeps Op.
	SizedOp Op
	// Axis pins the bound's keyword family, or AxisAuto to let the shape choose.
	Axis Axis
	// Param declares the key's arity.
	Param ParamMode
}

// Params is a key's bound parameter list. It exists so the only route from a
// raw tag value to an applier runs through [Bind], which enforces the row's
// declared arity and the operation's own well-formedness.
type Params struct {
	values []string
}

// ParamsOf builds a parameter list directly, for the narrow cases where a
// front-end has already resolved a value its own grammar owns (the jsonschema
// tag's empty-string const on a string field, which its own non-empty guard
// admits by exception).
func ParamsOf(values ...string) Params { return Params{values: values} }

// Values returns the bound parameters.
func (p Params) Values() []string { return p.values }

// Len reports how many parameters are bound.
func (p Params) Len() int { return len(p.values) }

// One returns the sole parameter, or "" when none is bound. Every operation
// reaching an applier with a single-value arity has one.
func (p Params) One() string {
	if len(p.values) == 0 {
		return ""
	}

	return p.values[0]
}

// Rule is a bound operation ready to apply: the resolved operation, the axis it
// targets, and its parameters.
type Rule struct {
	Params Params
	Op     Op
	Axis   Axis
}

// Bind resolves a key-table row against the target's shape and its raw tag
// value, producing the [Rule] that [Apply] executes. It resolves the sized-shape
// operation swap, enforces the row's declared arity, and checks that the
// parameters are well-formed for the operation, so neither dialect can hand the
// model a value it never validated.
func Bind(rule KeyRule, sh Shape, raw string, hasValue bool) (Rule, error) {
	op := rule.Op
	if rule.SizedOp != OpUnset && sh.Form.isSized() {
		op = rule.SizedOp
	}

	params, err := bindParams(rule, raw, hasValue)
	if err != nil {
		return Rule{}, err
	}

	err = checkParams(op, params)
	if err != nil {
		return Rule{}, err
	}

	return Rule{Op: op, Axis: rule.Axis, Params: params}, nil
}

// bindParams enforces the row's declared arity against the raw value.
func bindParams(rule KeyRule, raw string, hasValue bool) (Params, error) {
	switch rule.Param {
	case ParamNone:
		if hasValue {
			return Params{}, fmt.Errorf("takes no parameter, got %q", raw)
		}

		if rule.Implied == "" {
			return Params{}, nil
		}

		return Params{values: []string{rule.Implied}}, nil

	case ParamRequired:
		if !hasValue || raw == "" {
			return Params{}, errors.New("requires a non-empty value")
		}

		return Params{values: []string{raw}}, nil

	case ParamList:
		if !hasValue {
			return Params{}, errors.New("requires at least one value")
		}

		values := rule.Split(raw)
		if len(values) == 0 {
			return Params{}, errors.New("requires at least one value")
		}

		return Params{values: values}, nil

	default:
		return Params{}, fmt.Errorf("unknown parameter mode %d", rule.Param)
	}
}

// checkParams verifies the parameters suit the operation: a count for every
// operation that pins one value, at least one member for an enumeration, and a
// boolean literal for the uniqueness flag.
func checkParams(op Op, params Params) error {
	if op == OpOneOf {
		if params.Len() == 0 {
			return errors.New("requires at least one value")
		}

		return nil
	}

	if op == OpNonZero {
		// The non-zero assertion derives its own comparison value from the
		// shape, so it binds nothing.
		return nil
	}

	if params.Len() != 1 {
		return fmt.Errorf("expects exactly one value, got %d", params.Len())
	}

	if op == OpUnique {
		// Only the two literals both dialects spell, not strconv.ParseBool's
		// wider "1"/"t"/"TRUE" set, which neither tag grammar accepts.
		_, err := ParseBoolLiteral(params.One())
		if err != nil {
			return err
		}
	}

	return nil
}
