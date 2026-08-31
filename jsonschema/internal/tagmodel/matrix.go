package tagmodel

import (
	"fmt"
	"sort"
	"strings"
)

// cellStatus is what the model does with one (operation, shape) pair.
type cellStatus uint8

const (
	// The cellUnset status is the zero value: a pair nobody decided. The
	// init-time walk panics on it, which is the whole point of indexing rather
	// than looking up.
	cellUnset cellStatus = iota
	// The statusApply status runs the cell's applier.
	statusApply
	// The statusIgnore status is a deliberate, documented no-op: the tag means
	// something real, and the schema vocabulary has nothing faithful to emit.
	statusIgnore
	// The statusReject status reports [ErrUnsupported] carrying the cell's
	// written reason, which the front-end wraps with its own dialect prefix.
	statusReject
	// The statusCount constant bounds the label table.
	statusCount
)

// cell is one (operation, shape) decision.
type cell struct {
	apply  func(Target, Rule, Policy) error
	why    string
	status cellStatus
}

var (
	// The statusNames table labels each status for the golden matrix dump.
	statusNames = [statusCount]string{
		cellUnset:    "UNSET",
		statusApply:  "apply",
		statusIgnore: "ignore",
		statusReject: "reject",
	}

	// The matrix is the total dispatch table. Indexing a fixed-size array rather than
	// looking up a map is what makes a missing decision impossible to express: a new
	// operation grows every column and a new form grows every row, and the
	// init-time walk reports whatever was left behind.
	matrix [opCount][formCount]cell

	// The formAxis table is the keyword family a bound targets when the dialect named a rule
	// rather than a keyword, chosen by what the instance is. A form with no
	// natural axis holds AxisAuto, which resolveAxis reports rather than
	// guessing.
	formAxis = [formCount]Axis{
		FormString:     AxisLength,
		FormNumber:     AxisNumeric,
		FormArray:      AxisItems,
		FormObject:     AxisProperties,
		FormByteString: AxisItems, // A byte slice's rule-shaped bound reads as a count, which it has none of.
		// A text-marshaled value has no rule-shaped bound at all: go-playground's
		// min on a struct is not a length, and guessing one would invent a
		// constraint the author did not write. The pinned minLength still applies,
		// since the instance really is a string.
		FormTextString:    AxisAuto,
		FormBool:          AxisAuto,
		FormCoercedNumber: AxisAuto,
		FormCoercedBool:   AxisAuto,
		FormRawBytes:      AxisAuto,
		FormOpaque:        AxisAuto,
		FormUnset:         AxisAuto,
		// A referenced definition has no natural axis either: a rule-shaped
		// bound would have to guess which family the definition declares, and
		// guessing is what this column exists not to do. A named keyword still
		// applies, per formCarriesAxis below.
		FormRef: AxisAuto,
		// A declared object is an object instance, but not a map: go-playground
		// defines min on a map as an entry count and on a struct not at all, so
		// a rule-shaped bound reports here while the named property-count
		// keywords still apply, per formCarriesAxis below.
		FormDeclaredObject: AxisAuto,
	}

	// The formCarriesAxis table reports whether a form's schema has a keyword family at
	// all, so a dialect that pins the family gets an error instead of an inert
	// keyword. A byte slice is the interesting row: its base64 string has a
	// length, so minLength constrains it, but it has no array, so a count does
	// not.
	formCarriesAxis = [formCount][axisCount]bool{
		FormString:     {AxisLength: true},
		FormNumber:     {AxisNumeric: true},
		FormArray:      {AxisItems: true},
		FormObject:     {AxisProperties: true},
		FormTextString: {AxisLength: true},
		FormByteString: {AxisLength: true},
		// Every family, because the definition may declare any of them and this
		// dialect named the keyword outright.
		FormRef: {AxisNumeric: true, AxisLength: true, AxisItems: true, AxisProperties: true},
		// Only the property count: the payload declares an object outright, so
		// a named count keyword lands on it exactly as it does on the
		// $defs-backed named spelling, and every other family is a shape the
		// instance is known not to take.
		FormDeclaredObject: {AxisProperties: true},
	}

	// The formAxisNote table is what a form says when it cannot carry a bound,
	// for the forms whose reason is worth more than the generated one. It sits
	// beside the two tables above so a new form declares why it rejects a bound
	// where it declares which bounds it takes, rather than in a switch elsewhere.
	formAxisNote = [formCount]string{
		FormByteString: "a bound on a []byte field has no array length to constrain " +
			"(it encodes as a base64 string)",
	}
)

// coercedBoundNote explains why no bound reaches a string-coerced field. It is
// a static cell reason rather than a formAxisNote row: no cell routes a coerced
// form to applyBound, so the axis check never sees one.
const coercedBoundNote = "a bound is not supported on a json:\",string\" coerced numeric field " +
	"(minimum constrains JSON numbers, and no keyword constrains the magnitude of a string)"

// FormCarriesAxis reports whether a form's schema has a given keyword family at
// all. It is the table [resolveAxis] consults, exported because a dialect can
// have to answer the same question before a rule reaches the model: the
// jsonschema tag's type= pair drops every keyword family the new type has no
// use for, and which families those are is this fact and not a second one.
// Reading it here is the opposite of re-deriving it.
func FormCarriesAxis(f Form, a Axis) bool {
	if f >= formCount || a >= axisCount {
		return false
	}

	return formCarriesAxis[f][a]
}

// axisRejection explains why a bound cannot land on a form, and is the message
// the front-end wraps.
func axisRejection(form Form, axis Axis) string {
	if note := formAxisNote[form]; note != "" {
		return note
	}

	if axis == AxisAuto {
		return fmt.Sprintf("a %s has no bound keyword to carry", form)
	}

	return fmt.Sprintf("a %s cannot carry a %s bound", form, axis)
}

// init fills the matrix and then walks it. Every row starts fully rejected with
// a generated reason, so a pair nobody thought about is a documented error
// rather than a gap; the overrides below are the decisions. The walk runs on
// first import, so every test binary in the module trips a mistake.
func init() {
	for op := OpUnset + 1; op < opCount; op++ {
		for f := FormUnset + 1; f < formCount; f++ {
			matrix[op][f] = cell{
				status: statusReject,
				why:    fmt.Sprintf("%s is not supported on a %s", op, f),
			}
		}
	}

	fillBounds()
	fillValues()
	fillNonZero()
	fillStringKeywords()

	verifyMatrix()
}

// fillBounds fills the six bound rows. Every form that carries any axis takes
// the shared bound applier; resolveAxis has already rejected the pairs whose
// axis does not exist, so a cell reached here has a family to write.
func fillBounds() {
	bounded := []Form{
		FormString, FormNumber, FormArray, FormObject, FormTextString, FormByteString, FormRef,
		FormDeclaredObject,
	}

	for _, op := range []Op{OpFloorIncl, OpFloorExcl, OpCeilIncl, OpCeilExcl, OpExactSize} {
		for _, f := range bounded {
			apply(op, f, applyBound)
		}
	}

	// An exact size pins the value itself on a numeric shape rather than a
	// length: go-playground defines len=N on a number as "equals N".
	apply(OpExactSize, FormNumber, applyEqual)
	apply(OpExactSize, FormCoercedNumber, applyEqual)

	// Forbidding a size is only meaningful where a size exists, and which size
	// it is comes from the table rather than from the applier.
	apply(OpForbidSize, FormArray, forbidSizeOn(AxisItems))
	apply(OpForbidSize, FormObject, forbidSizeOn(AxisProperties))

	// A coerced numeric bound has no faithful mapping: minimum constrains JSON
	// numbers, so it is inert against the quoted string the field emits, and
	// JSON Schema has no keyword for "the numeric value of this text is >= N".
	for _, op := range []Op{OpFloorIncl, OpFloorExcl, OpCeilIncl, OpCeilExcl} {
		for _, f := range []Form{FormCoercedNumber, FormCoercedBool} {
			reject(op, f, coercedBoundNote)
		}
	}
}

// fillValues fills the value rows: pinning, forbidding, enumerating, uniqueness,
// and the divisor.
func fillValues() {
	scalars := []Form{FormString, FormNumber, FormBool, FormCoercedNumber, FormCoercedBool}

	for _, f := range scalars {
		apply(OpEqual, f, applyEqual)
		apply(OpNotEqual, f, applyNotEqual)
		apply(OpOneOf, f, applyOneOf)
	}

	// An enumeration on a sequence enumerates each element, not the array value,
	// so it retargets. Both dialects mean the same thing by it, and both reach
	// the elements through one path.
	apply(OpOneOf, FormArray, retargetToElements)

	// A text-marshaled string has no way to spell its own value in a tag: the
	// author would be writing the marshaled form of a type the tag cannot
	// construct, so a scalar comparison is rejected while a format still
	// applies.
	for _, op := range []Op{OpEqual, OpNotEqual, OpOneOf} {
		reject(op, FormTextString,
			"a text-marshaled value cannot be compared against a tag literal "+
				"(the tag has no way to spell the marshaled form)")
	}

	// A bare enumeration on a map has no go-playground element meaning, unlike a
	// dive, which says "descend" explicitly. Keeping the asymmetry is deliberate.
	reject(OpOneOf, FormObject,
		"an enumeration on a map has no element meaning (use dive to constrain the values)")

	// A byte slice encodes as one base64 string, so an element enumeration has
	// nothing to land on. Rejecting it rather than dropping it silently is the
	// same policy the length constraints on a byte slice already follow, and it
	// is what makes a sequence-wide enumeration and a dive-scoped one agree.
	for _, f := range []Form{FormByteString, FormRawBytes} {
		reject(OpOneOf, f, NoElementsReason(f))
	}

	apply(OpUnique, FormArray, applyUnique)

	// Only an array can carry uniqueItems. Naming that in the rejection matters
	// because the mistake is usually a shape mistake, not a spelling one.
	for f := FormUnset + 1; f < formCount; f++ {
		if f != FormArray && f != FormObject {
			reject(OpUnique, f, fmt.Sprintf("unique has no array to constrain on a %s", f))
		}
	}

	// The go-playground unique on a map asserts its values are distinct: a real
	// constraint with no object-side counterpart to uniqueItems, so there is
	// nothing faithful to emit and nothing mistaken about having written it.
	ignore(OpUnique, FormObject,
		"unique on a map asserts distinct values, which no object keyword expresses")

	apply(OpMultipleOf, FormNumber, applyMultipleOf)
	apply(OpMultipleOf, FormRef, applyMultipleOf)
}

// fillNonZero fills the non-zero row, whose shape depends entirely on what
// "empty" means for the instance.
func fillNonZero() {
	apply(OpNonZero, FormString, nonZeroFloor(AxisLength))
	apply(OpNonZero, FormByteString, nonZeroFloor(AxisLength))
	apply(OpNonZero, FormArray, nonZeroFloor(AxisItems))
	apply(OpNonZero, FormObject, nonZeroFloor(AxisProperties))

	apply(OpNonZero, FormNumber, nonZeroForbidNumber)
	apply(OpNonZero, FormCoercedNumber, nonZeroForbidCoerced)
	apply(OpNonZero, FormCoercedBool, nonZeroForbidCoerced)

	apply(OpNonZero, FormBool, nonZeroTrue)

	// A raw byte slice's schema admits any JSON value, so neither half of the
	// non-zero assertion is faithful. A minItems would reject the empty array a
	// non-nil json.RawMessage holding "[]" legitimately carries, and a forbidden
	// null would reject the one holding "null", which go-playground reads as the
	// non-nil slice it is. Only the parent's required entry applies, which the
	// front-end adds separately.
	ignore(OpNonZero, FormRawBytes,
		"an unconstrained raw JSON value has no faithful non-zero form, not even a forbidden null")

	// A text-marshaled or opaque value has no emptiness the schema can name.
	// A declared object is in the same position: its Go value is a struct or
	// override whose zero is not the empty object a minProperties floor would
	// assert against, so only the parent's required entry applies.
	ignore(OpNonZero, FormTextString, "a text-marshaled value has no schema-expressible zero")
	ignore(OpNonZero, FormOpaque, "an opaque value has no schema-expressible zero")
	ignore(OpNonZero, FormDeclaredObject,
		"a declared object's zero is not the empty object; only the parent's required entry applies")
	ignore(OpNonZero, FormRef,
		"a referenced definition's zero is not readable here; only the parent's required entry applies")
}

// fillStringKeywords fills the four first-wins string-keyword rows, which apply
// wherever the instance is a string.
func fillStringKeywords() {
	for op := OpUnset + 1; op < opCount; op++ {
		if !op.IsStringKeyword() {
			continue
		}

		for _, f := range []Form{
			FormString, FormTextString, FormByteString, FormCoercedNumber, FormCoercedBool, FormRef,
		} {
			apply(op, f, applyStringKeyword)
		}
	}

	// A raw JSON value never carries encoded content: contentMediaType and
	// contentEncoding describe a string holding an encoded document, and a
	// json.RawMessage instance is whatever JSON value it holds, already
	// decoded. The go-playground json and base64 rules on a RawMessage are
	// real runtime checks over the raw bytes with nothing faithful to emit -- the
	// same policy the non-zero row takes on this form -- and emitting the
	// keyword would be worse than inert: a raw value that happens to be a JSON
	// string would be asserted to hold an encoded document it never claimed.
	for _, op := range []Op{OpContentMediaType, OpContentEncoding} {
		ignore(op, FormRawBytes,
			"a raw JSON value is already decoded JSON, not a string carrying encoded content")
	}
}

// apply records an applying cell.
func apply(op Op, f Form, fn func(Target, Rule, Policy) error) {
	matrix[op][f] = cell{status: statusApply, apply: fn}
}

// reject records a rejecting cell with its message.
func reject(op Op, f Form, why string) {
	matrix[op][f] = cell{status: statusReject, why: why}
}

// ignore records a deliberate no-op with the reason it is one.
func ignore(op Op, f Form, why string) {
	matrix[op][f] = cell{status: statusIgnore, why: why}
}

// verifyMatrix is the init-time totality guard. It panics naming any pair left
// unfilled, any applying cell with no applier, and any ignored or rejected cell
// with no written reason, so a decision cannot be omitted or left unexplained.
// It also checks the axis tables, whose rows the bound cells depend on.
func verifyMatrix() {
	for op := OpUnset + 1; op < opCount; op++ {
		for f := FormUnset + 1; f < formCount; f++ {
			c := matrix[op][f]

			switch c.status {
			case cellUnset:
				panic(fmt.Sprintf("tagmodel: no decision for (%s, %s)", op, f))
			case statusApply:
				if c.apply == nil {
					panic(fmt.Sprintf("tagmodel: (%s, %s) applies with no applier", op, f))
				}

			case statusIgnore, statusReject:
				if c.why == "" {
					panic(fmt.Sprintf("tagmodel: (%s, %s) is %s with no reason",
						op, f, statusNames[c.status]))
				}

			case statusCount:
				panic(fmt.Sprintf("tagmodel: (%s, %s) holds the count sentinel", op, f))
			}
		}
	}

	for f := FormUnset + 1; f < formCount; f++ {
		// The auto axis is a request to choose, never a family a schema has.
		//
		// A form's natural axis and the axes it carries are deliberately allowed
		// to disagree, and two rows depend on it: a byte slice's rule-shaped
		// bound means the item count it has none of, while the base64 string it
		// does have carries a length, and a text-marshaled value has no
		// rule-shaped bound at all while still carrying minLength. That
		// disagreement is exactly what makes min on a []byte an error and
		// minLength on one not.
		if formCarriesAxis[f][AxisAuto] {
			panic(fmt.Sprintf("tagmodel: %s claims to carry the auto axis", f))
		}
	}
}

// DumpMatrix renders the whole table as one line per pair, for the golden test
// that makes a deliberate cell change a reviewable diff and an accidental one a
// failure.
func DumpMatrix() string {
	var lines []string

	for op := OpUnset + 1; op < opCount; op++ {
		for f := FormUnset + 1; f < formCount; f++ {
			c := matrix[op][f]

			line := fmt.Sprintf("%s / %s -> %s", op, f, statusNames[c.status])
			if c.why != "" {
				line += ": " + c.why
			}

			lines = append(lines, line)
		}
	}

	sort.Strings(lines)

	return strings.Join(lines, "\n") + "\n"
}
