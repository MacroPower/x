package jsonschema

// Draft represents a JSON Schema draft version. Older drafts compare as
// less than newer ones, so ordering comparisons are meaningful; the numeric
// values themselves are not part of the API and may change between releases
// (they are spaced so a future draft can slot between existing ones), so a
// Draft must not be persisted or transmitted as an integer.
type Draft int

const (
	// Draft2020 targets JSON Schema Draft 2020-12
	// (https://json-schema.org/draft/2020-12/schema). It is the zero value
	// and the default.
	Draft2020 Draft = 0

	// Draft7 targets JSON Schema Draft-07
	// (http://json-schema.org/draft-07/schema#). It sorts before Draft2020
	// so older drafts compare as less than newer ones, with room left
	// between them for the drafts in between (2019-09).
	Draft7 Draft = -100
)

// DraftOption is the option type returned by [WithDraft]: a single option
// value that configures generation ([GenerateOption]), validation
// ([ValidateOption]), and inlining ([InlineOption]) alike, the way
// [ResolverOption] serves validation and inlining.
type DraftOption interface {
	GenerateOption
	ValidateOption
	InlineOption
}

// draftOption is the [DraftOption] returned by [WithDraft].
type draftOption struct {
	d Draft
}

func (o draftOption) applyGenerate(g *generator) { g.draft = o.d }

func (o draftOption) applyValidate(v *validator) { v.draftOverride = &o.d }

func (o draftOption) applyInline(in *inliner) { in.draftOverride = &o.d }

// WithDraft sets the JSON Schema draft version. The returned option serves
// generation, validation, and inlining alike.
//
// During generation it selects the target draft the schema is produced for
// (default: [Draft2020]). During validation and inlining it overrides the
// draft otherwise detected from the root schema's $schema field, for schemas
// that omit $schema (which would default to [Draft2020]) or carry one that
// does not reflect the dialect they are written in; the $schema field itself
// is not modified.
func WithDraft(d Draft) DraftOption {
	return draftOption{d: d}
}

// schemaURI returns the $schema URI for the draft. An unrecognized draft
// returns the empty string rather than silently defaulting to a known URI.
func (d Draft) schemaURI() string {
	switch d {
	case Draft7:
		return "http://json-schema.org/draft-07/schema#"
	case Draft2020:
		return "https://json-schema.org/draft/2020-12/schema"
	default:
		return ""
	}
}

// refPrefix returns the $ref path prefix for the draft's definitions section:
// "#/$defs/" for Draft 2020-12, or "#/definitions/" for Draft-07. An
// unrecognized draft uses the Draft 2020-12 prefix, consistent with schemaURI
// returning an empty $schema for unknown drafts: a document without $schema
// defaults to the latest draft, whose definitions live under $defs.
func (d Draft) refPrefix() string {
	if d == Draft7 {
		return "#/definitions/"
	}

	return "#/$defs/"
}
