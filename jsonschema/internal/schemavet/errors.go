package schemavet

import (
	"errors"

	"go.jacobcolvin.com/x/jsonschema/internal/format"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// The vetting sentinels live here, beside the checks that mint them, and are
// re-exported from the parent package's errors.go (the same convention
// internal/refresolve uses for ErrNotResolved and ErrRefResolve), so
// [errors.Is] matches each sentinel identically whether a failure originates
// in this package or in the parent. The parent-side re-exports carry the full
// public doc comments; the values here are the single source of truth.
var (
	// ErrInvalidType reports a type keyword naming something other than the
	// seven JSON Schema type names. It is the sentinel
	// [typename.ErrInvalidType] the struct tag's type= check mints too.
	ErrInvalidType = typename.ErrInvalidType

	// ErrItemsArrayUnderDraft2020 reports the array form of the items keyword
	// under Draft 2020-12, where it has no meaning.
	ErrItemsArrayUnderDraft2020 = errors.New("array-form items is not valid under draft 2020-12")

	// ErrNegativeBound reports a negative value on a length or count keyword.
	ErrNegativeBound = errors.New("negative bound")

	// ErrInvalidPattern reports a pattern or a patternProperties key that is
	// not an ECMA-262 regular expression. It is [format.ErrInvalidRegex], so
	// a tag reading such a pattern and a document carrying one report the
	// one sentinel.
	ErrInvalidPattern = format.ErrInvalidRegex

	// ErrNonPositiveMultipleOf reports a multipleOf value not strictly
	// greater than zero.
	ErrNonPositiveMultipleOf = errors.New("multipleOf must be greater than 0")

	// ErrNilSubschema reports a nil *Schema element inside a sub-schema slice
	// or map.
	ErrNilSubschema = errors.New("nil subschema")

	// ErrConflictingSchemaFields reports a schema setting both Go fields that
	// spell one JSON keyword.
	ErrConflictingSchemaFields = errors.New("conflicting schema fields")

	// ErrDuplicatePropertyOrder reports a PropertyOrder slice listing the
	// same property twice.
	ErrDuplicatePropertyOrder = errors.New("duplicate propertyOrder entry")

	// ErrInvalidID reports an $id outside the keyword's domain.
	ErrInvalidID = errors.New("invalid $id")

	// ErrMisplacedVocabulary reports a $vocabulary on a node whose $schema
	// does not establish the Draft 2020-12 dialect.
	ErrMisplacedVocabulary = errors.New("misplaced $vocabulary")

	// ErrUnknownFormat reports a format keyword naming a format the run holds
	// no checker for, under a run the 2020-12 format-assertion vocabulary
	// governs, where validation section 7.2.3 requires an implementation to
	// fail upon encountering an unknown format. [Profile.KnownFormat] turns
	// the check on.
	ErrUnknownFormat = errors.New("unknown format")

	// ErrSchemaCycle reports a schema graph holding a loop that crosses a
	// schema, which has no tree form and which upstream's MarshalJSON
	// recurses into without bound.
	ErrSchemaCycle = errors.New("schema graph holds a cycle")

	// ErrIDCollision reports a document claiming an identifier another
	// document already holds, or one node of a document claiming an
	// identifier through two positions. Two schemas under one identifier
	// leave every reference naming it ambiguous, so the claim is refused
	// rather than a winner picked. The refresolve package re-exports it for
	// the cross-document case, and the parent package re-exports it
	// publicly, so [errors.Is] matches through every name.
	ErrIDCollision = errors.New("identifier collision")
)
