package tagmodel

import (
	"reflect"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

// The size-literal domains, re-exported from the shared algebra that owns them
// so a front-end names its policy without reaching past the model.
const (
	// SizeFold folds a negative literal into the unsatisfiable range.
	SizeFold = constraint.SizeFold
	// SizeStrict rejects a negative literal outright.
	SizeStrict = constraint.SizeStrict
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
	Sizes constraint.SizeDomain
	// Keywords is how a string keyword composes with one already in force.
	Keywords KeywordPolicy
	// AllowNullScalar admits the literal "null" as the JSON null value on a
	// nullable shape. The jsonschema tag spells null that way; the validate tag
	// has no null literal, so there "null" is the four-character string.
	AllowNullScalar bool
	// NamedKeywords marks a dialect whose keys name JSON Schema keywords
	// outright rather than runtime rules. An ignored cell -- a real rule the
	// schema vocabulary has nothing faithful to emit for -- then reports
	// [ErrUnsupported] instead of dropping the key: a rule-shaped dialect's
	// constraint still holds at runtime when nothing is emitted, but an author
	// who named a keyword asked for that keyword, and a silent drop would
	// leave text that reads as a constraint nothing enforces.
	NamedKeywords bool
}
