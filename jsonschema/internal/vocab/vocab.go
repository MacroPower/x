// Package vocab models JSON Schema Draft 2020-12 vocabularies: the standard
// vocabulary URIs, the resolved set of active keyword groups for a validation
// run, and resolution of a raw $vocabulary map into that set.
package vocab

import "slices"

// Standard vocabulary URIs for JSON Schema Draft 2020-12.
const (
	Core2020             = "https://json-schema.org/draft/2020-12/vocab/core"
	Applicator2020       = "https://json-schema.org/draft/2020-12/vocab/applicator"
	Validation2020       = "https://json-schema.org/draft/2020-12/vocab/validation"
	Unevaluated2020      = "https://json-schema.org/draft/2020-12/vocab/unevaluated"
	Content2020          = "https://json-schema.org/draft/2020-12/vocab/content"
	FormatAnnotation2020 = "https://json-schema.org/draft/2020-12/vocab/format-annotation"
	FormatAssertion2020  = "https://json-schema.org/draft/2020-12/vocab/format-assertion"
	MetaData2020         = "https://json-schema.org/draft/2020-12/vocab/meta-data"
)

// knownVocabularies is the set of all vocabulary URIs this implementation
// recognizes. Unrecognized required vocabularies cause validation to fail.
var knownVocabularies = map[string]bool{
	Core2020:             true,
	Applicator2020:       true,
	Validation2020:       true,
	Unevaluated2020:      true,
	Content2020:          true,
	FormatAnnotation2020: true,
	FormatAssertion2020:  true,
	MetaData2020:         true,
}

// Set is the resolved set of active vocabularies for a validation run.
// Named bools avoid map lookups in the hot validation path, so it tracks only
// the groups consulted there: applicator, validation, unevaluated, and
// format-assertion. The content vocabulary also gates a keyword group, but it
// runs off the hot path and is tracked separately on the validator. Core is
// always required and meta-data carries no assertion behavior, so neither
// needs a field here.
type Set struct {
	Applicator      bool
	Validation      bool
	Unevaluated     bool
	FormatAssertion bool
}

// All returns the default Set for a standard draft: every keyword group
// active except format-assertion, which is annotation-only by default
// (validation §7.2.1). Format assertion is opted into via the
// format-assertion vocabulary or the parent package's WithFormats option.
func All() Set {
	return Set{
		Applicator:      true,
		Validation:      true,
		Unevaluated:     true,
		FormatAssertion: false,
	}
}

// Resolve converts a raw $vocabulary map to a Set.
func Resolve(vocabs map[string]bool) Set {
	vs := Set{}
	for uri, active := range vocabs {
		// The format-assertion vocabulary is special: once an implementation
		// recognizes it, format is asserted regardless of the true/false value.
		// The boolean only governs implementations that do not understand the
		// vocabulary (validation §7.2.2), so its mere presence enables assertion.
		if uri == FormatAssertion2020 {
			vs.FormatAssertion = true
			continue
		}

		if !active {
			continue
		}

		switch uri {
		case Applicator2020:
			vs.Applicator = true
		case Validation2020:
			vs.Validation = true
		case Unevaluated2020:
			vs.Unevaluated = true
		}
	}

	return vs
}

// CheckUnknown returns the smallest required (true) URI in the $vocabulary map
// that this implementation does not recognize, or "" when every required
// vocabulary is known. Optional (false) unknown vocabularies are silently
// ignored. The smallest is chosen so the reported URI is deterministic when a
// document declares more than one unknown required vocabulary, rather than
// depending on map iteration order.
func CheckUnknown(vocabs map[string]bool) string {
	var unknown []string

	for uri, required := range vocabs {
		if required && !knownVocabularies[uri] {
			unknown = append(unknown, uri)
		}
	}

	if len(unknown) == 0 {
		return ""
	}

	return slices.Min(unknown)
}
