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
// [RefOption] serves validation and inlining.
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

// draftProfile declares every value-level behavioral difference between drafts
// as data, so a difference is stated once in [draftProfiles] rather than
// re-derived at each site that compares a run's draft. Keyword *applicability*
// (which keywords a draft evaluates at all) is declared per keyword in
// internal/keywordmeta and derived onto the dispatch table's rows; the profile
// carries the finer policy the walk, the inliner,
// and the generator would otherwise each branch on. Each field is kept distinct
// even where several currently share the value "this is Draft 2020-12", because
// they name independent policies that a future draft could set independently.
//
// Every field is a bool, so the struct carries no pointers and packs into an
// engine struct without disturbing its pointer layout; the derived $ref prefix
// is a method ([draftProfile.refPrefix]) rather than a stored string.
type draftProfile struct {
	// The honorRefSiblings flag reports whether keywords beside a $ref apply.
	// Draft 2020-12 evaluates a $ref alongside its siblings; Draft-07 ignores
	// them.
	honorRefSiblings bool
	// The dynamicRef flag reports whether $dynamicRef and the dynamic scope it
	// resolves against exist. They are a Draft 2020-12 addition; Draft-07 has
	// neither.
	dynamicRef bool
	// The prefixItemsTuple flag reports whether tuple validation is spelled with
	// prefixItems (2020-12) rather than the array form of items (Draft-07).
	prefixItemsTuple bool
	// The rejectItemsArray flag reports whether the array form of items is a
	// structural error. It has no meaning under 2020-12 (tuples use
	// prefixItems), and is the Draft-07 tuple spelling, so only 2020-12 rejects
	// it.
	rejectItemsArray bool
	// The containsCounts flag reports whether minContains/maxContains are
	// keywords. They are a Draft 2020-12 addition; Draft-07's contains carries
	// only its default floor of one match.
	containsCounts bool
	// The vocabularies flag reports whether the $vocabulary concept applies.
	// Draft-07 predates it and always runs the full vocabulary set.
	vocabularies bool
	// The formatAssertsByDefault flag reports whether format is asserted without
	// an opt-in. Draft-07 asserts it (validation section 7.2 permits it);
	// 2020-12 asserts only when the format-assertion vocabulary is active.
	formatAssertsByDefault bool
	// The definitionsKeyword flag reports whether generation emits the
	// reusable-schema map as "definitions" (Draft-07) rather than "$defs"
	// (2020-12). It also selects the $ref prefix (see refPrefix).
	definitionsKeyword bool
	// The closeWithUnevaluated flag reports whether generation closes an
	// allOf-composed object with unevaluatedProperties (2020-12) rather than
	// omitting additionalProperties (Draft-07, which lacks unevaluated*).
	closeWithUnevaluated bool
}

// refPrefix returns the $ref path prefix for the draft's definitions section:
// "#/definitions/" for a draft whose reusable-schema map is spelled
// "definitions" (Draft-07), "#/$defs/" otherwise (Draft 2020-12). It derives
// from definitionsKeyword so the two stay in lockstep.
func (p draftProfile) refPrefix() string {
	if p.definitionsKeyword {
		return "#/definitions/"
	}

	return "#/$defs/"
}

// draftProfiles is the per-draft policy table: one row states every behavioral
// difference for that draft. Adding a draft is one new row, and the derived
// call sites pick it up.
var draftProfiles = map[Draft]draftProfile{
	Draft2020: {
		honorRefSiblings:       true,
		dynamicRef:             true,
		prefixItemsTuple:       true,
		rejectItemsArray:       true,
		containsCounts:         true,
		vocabularies:           true,
		formatAssertsByDefault: false,
		definitionsKeyword:     false,
		closeWithUnevaluated:   true,
	},
	Draft7: {
		honorRefSiblings:       false,
		dynamicRef:             false,
		prefixItemsTuple:       false,
		rejectItemsArray:       false,
		containsCounts:         false,
		vocabularies:           false,
		formatAssertsByDefault: true,
		definitionsKeyword:     true,
		closeWithUnevaluated:   false,
	},
}

// profile returns the [draftProfile] for the draft. An unrecognized draft falls
// back to the Draft 2020-12 row: a document without $schema defaults to the
// latest draft, whose reusable schemas live under $defs.
func (d Draft) profile() draftProfile {
	if p, ok := draftProfiles[d]; ok {
		return p
	}

	return draftProfiles[Draft2020]
}
