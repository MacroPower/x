package tagmodel

import "reflect"

// SizePolicy is how a dialect reads a length or count literal. The two dialects
// disagree for a defensible reason on each side, so the choice is a named
// parameter rather than a second implementation.
type SizePolicy uint8

const (
	// SizeStrict rejects a negative literal outright, matching the non-negative
	// value domain of minLength and its sibling keywords: a dialect that names
	// the keyword is writing that keyword's value, and -1 is not one.
	SizeStrict SizePolicy = iota
	// SizeFold folds a negative literal through the shared size algebra, which
	// expresses it as the unsatisfiable floor-one/ceiling-zero range. A dialect
	// naming a rule rather than a keyword is describing a predicate, and
	// go-playground's max=-1 is a predicate no value satisfies.
	SizeFold
)

// Policy carries the divergences that are properly dialect-specific, so they
// are named parameters of one implementation rather than grounds for two.
type Policy struct {
	// BoundKind is the Go kind a numeric bound literal parses at.
	// [reflect.Invalid] takes the keyword-shaped domain: minimum is a JSON
	// Schema keyword whose value may be fractional even on an integer schema, so
	// minimum=1.5 on an int8 is correct. A real kind takes the Go-shaped domain,
	// which is what go-playground does: it parses gte=1.5 at the field kind, so
	// on an int that is an error. Same literal, two right answers.
	BoundKind reflect.Kind
	// Sizes is how a length or count literal reads.
	Sizes SizePolicy
	// AllowNullScalar admits the literal "null" as the JSON null value on a
	// nullable shape. The jsonschema tag spells null that way; the validate tag
	// has no null literal, so there "null" is the four-character string.
	AllowNullScalar bool
}
