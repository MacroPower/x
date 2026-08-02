// Package tagmodel is the one interpretation of a struct-tag constraint, shared
// by every tag dialect the generator understands.
//
// The package sits between the dialect front-ends (internal/tagparse for the
// jsonschema tag, interpreters/validate for the go-playground validate tag) and
// internal/constraint, which owns the bound algebra beneath it:
//
//	dialect front-ends   internal/tagparse          interpreters/validate
//	                            \                        /
//	                             v                      v
//	the typed model                   internal/tagmodel
//	                                          |
//	                                          v
//	the bound algebra                 internal/constraint
//
// A front-end owns its grammar -- how a tag is split, how a list is spelled,
// which control words it skips, how its errors read -- and nothing else. It
// classifies the field into a [Shape], looks the key up in its own table of
// [KeyRule] rows, binds the parameter with [Bind], and hands the resulting
// [Rule] to [Apply]. Everything from there on is one implementation.
//
// # Form is the dispatch key
//
// [Form] is the JSON shape an instance actually takes, not the Go kind. That is
// the central move: coercion stops being a pre-pass gate every operation has to
// remember and becomes a column of the dispatch table. A numeric Go kind whose
// schema is a string -- a json:",string" field, or a type marshaling itself as
// text -- classifies as [FormCoercedNumber] at construction, so no operation can
// reach an applier without having been told, and no applier has to ask whether
// it is looking at a coerced value.
//
// The classification reads the type-derived base schema, not only the Go kind,
// because the two can legitimately disagree: a verbatim type schema, a type=
// override, or a provider can make a string-kinded field's instance a number.
// The base is the authority on what the instance is; the Go kind fills in what
// the base leaves open, and is the only thing that can tell a coerced shape from
// a native one.
//
// # The matrix is total by construction
//
// [Apply] dispatches on the pair (Op, Form) through a fixed-size array. Every
// cell is Apply, Ignore, or Reject, and Ignore and Reject each carry a written
// reason. Three guards escalate:
//
//  1. Structural: the table is [opCount][formCount]cell, so adding an operation
//     or a form grows every row and column. There is no lookup that can miss.
//  2. Init-time: the package walks every cell on first import and panics naming
//     any pair left unfilled, any Apply cell with no applier, and any Ignore or
//     Reject cell with no reason. Every test binary in the module trips it.
//  3. Golden: TestMatrixGolden pins a textual dump of the whole table, so a
//     deliberate cell change is a reviewable diff and an accidental one fails.
//
// # What this forecloses
//
// Three fixes that shipped separately share one shape: a call site re-deriving
// shared policy instead of calling through one implementation, with nothing
// structurally requiring it to. Each is now unreachable rather than merely
// fixed:
//
//   - A sequence-wide rule that skipped the coercion gate the field-level path
//     applied, so oneof and dive,oneof disagreed on the same element type.
//     Coercion is a Form, an element target is classified by [ShapeOf] at
//     construction, and both paths re-enter [Apply].
//   - A non-zero check that hardcoded "0" and "false" instead of routing through
//     the shared serializer, leaving a string-marshaling type's check inert. The
//     coerced non-zero cells call [Shape.ParseScalar], the same function every
//     other coerced cell calls.
//   - A key whose parameter was never read, so unique=Name degraded to a bare
//     uniqueItems. Arity is a declared field on the key-table row and [Bind] is
//     the only route from a raw value to [Params].
//
// # Divergence is a parameter, not a second implementation
//
// Two dialect differences are legitimate and are named rather than erased.
// [Policy.BoundKind] carries the numeric-bound literal domain: the jsonschema
// tag parses a bound at [reflect.Invalid] because minimum is a keyword whose
// value may be fractional on an integer schema, while the validate tag parses at
// the field kind because go-playground does. Same literal, two right answers.
// [Policy.Sizes] carries the negative-size question: a keyword-shaped dialect
// rejects minLength=-1 because the keyword's value domain is non-negative, while
// go-playground's max=-1 folds into the unsatisfiable range. Anything a dialect
// spells differently -- whether a key takes a parameter, what a bare key
// implies, how a list is tokenized -- is a field on its own [KeyRule] row.
package tagmodel
