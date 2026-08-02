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

// KeywordPolicy is how a dialect's string keyword (format, pattern, content)
// composes with a value already in force. The package's documented precedence
// is jsonschema tag over type over interpreter, which is two behaviors, so it is
// a parameter rather than a choice the model makes for everyone.
type KeywordPolicy uint8

const (
	// KeywordReplace lets the value replace whatever is in force. It is the
	// jsonschema tag's rule: the tag names the keyword outright, so an author
	// writing pattern= means that pattern and not the type's.
	KeywordReplace KeywordPolicy = iota
	// KeywordFirstWins keeps a value already in force. It is a tag
	// interpreter's rule: an interpreter infers a keyword from a validator
	// rather than naming it, so it never overrides what was stated outright.
	KeywordFirstWins
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
	// Keywords is how a string keyword composes with one already in force.
	Keywords KeywordPolicy
	// AllowNullScalar admits the literal "null" as the JSON null value on a
	// nullable shape. The jsonschema tag spells null that way; the validate tag
	// has no null literal, so there "null" is the four-character string.
	AllowNullScalar bool
}
