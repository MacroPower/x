package fuzzgen

// TagParamKeys exposes the keys the jsonschema tag draw has parameters for,
// so a guard can hold the table to the parser's vocabulary.
func TagParamKeys() []string {
	keys := make([]string, 0, len(tagParams))
	for key := range tagParams {
		keys = append(keys, key)
	}

	return keys
}
