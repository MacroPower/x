package jsonschema

import "fmt"

// Standard vocabulary URIs for JSON Schema Draft 2020-12.
const (
	VocabCore2020             = "https://json-schema.org/draft/2020-12/vocab/core"
	VocabApplicator2020       = "https://json-schema.org/draft/2020-12/vocab/applicator"
	VocabValidation2020       = "https://json-schema.org/draft/2020-12/vocab/validation"
	VocabUnevaluated2020      = "https://json-schema.org/draft/2020-12/vocab/unevaluated"
	VocabContent2020          = "https://json-schema.org/draft/2020-12/vocab/content"
	VocabFormatAnnotation2020 = "https://json-schema.org/draft/2020-12/vocab/format-annotation"
	VocabFormatAssertion2020  = "https://json-schema.org/draft/2020-12/vocab/format-assertion"
	VocabMetaData2020         = "https://json-schema.org/draft/2020-12/vocab/meta-data"
)

// knownVocabularies is the set of all vocabulary URIs this implementation
// recognizes. Unrecognized required vocabularies cause validation to fail.
var knownVocabularies = map[string]bool{
	VocabCore2020:             true,
	VocabApplicator2020:       true,
	VocabValidation2020:       true,
	VocabUnevaluated2020:      true,
	VocabContent2020:          true,
	VocabFormatAnnotation2020: true,
	VocabFormatAssertion2020:  true,
	VocabMetaData2020:         true,
}

// vocabSet is the resolved set of active vocabularies for a validation run.
// Named bools avoid map lookups in the hot validation path. Only vocabularies
// that gate a keyword group behaviorally are tracked; core (always required),
// content and meta-data (never validated), and format-annotation (no assertion
// effect) are omitted because their active state has no bearing on the walk.
type vocabSet struct {
	applicator      bool
	validation      bool
	unevaluated     bool
	formatAssertion bool
}

// allVocabs returns the default vocabSet for a standard draft: every keyword
// group active except format-assertion, which is annotation-only by default
// (validation §7.2.1). Format assertion is opted into via the format-assertion
// vocabulary or [WithFormats].
func allVocabs() vocabSet {
	return vocabSet{
		applicator:      true,
		validation:      true,
		unevaluated:     true,
		formatAssertion: false,
	}
}

// resolveVocabs converts a raw $vocabulary map to a vocabSet.
func resolveVocabs(vocabs map[string]bool) vocabSet {
	vs := vocabSet{}
	for uri, active := range vocabs {
		// The format-assertion vocabulary is special: once an implementation
		// recognizes it, format is asserted regardless of the true/false value.
		// The boolean only governs implementations that do not understand the
		// vocabulary (validation §7.2.1), so its mere presence enables assertion.
		if uri == VocabFormatAssertion2020 {
			vs.formatAssertion = true
			continue
		}

		if !active {
			continue
		}

		switch uri {
		case VocabApplicator2020:
			vs.applicator = true
		case VocabValidation2020:
			vs.validation = true
		case VocabUnevaluated2020:
			vs.unevaluated = true
		}
	}

	return vs
}

// checkUnknownVocabularies returns ErrUnknownVocabulary if the $vocabulary
// map contains any required (true) URI that this implementation does not
// recognize. Optional (false) unknown vocabularies are silently ignored.
func checkUnknownVocabularies(vocabs map[string]bool) error {
	for uri, required := range vocabs {
		if required && !knownVocabularies[uri] {
			return fmt.Errorf("%w: %s", ErrUnknownVocabulary, uri)
		}
	}

	return nil
}
