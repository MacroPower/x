package jsonschema

// Draft represents a JSON Schema draft version.
type Draft int

const (
	// Draft2020 targets JSON Schema Draft 2020-12
	// (https://json-schema.org/draft/2020-12/schema). It is the zero value
	// and the default.
	Draft2020 Draft = 0

	// Draft7 targets JSON Schema Draft-07
	// (http://json-schema.org/draft-07/schema#). It sorts before Draft2020
	// so older drafts compare as less than newer ones.
	Draft7 Draft = -1
)

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
