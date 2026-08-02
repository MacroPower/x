package format_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Rig 3, layer 3 -- vendored conformance corpus for uri-template. The
// uri-templates/uritemplate-test suite is the corpus the RFC 6570
// implementations test against; it is vendored under
// testdata/corpus/uritemplate/ with its upstream LICENSE and a README recording
// provenance.
//
// The corpus tests template *expansion* against a variable binding, while the
// uri-template format asserts the template *grammar* and has no bindings to
// expand against. Element 0 of each test case is the template, and it is the
// only element this driver consumes. The two are not the same predicate
// everywhere: a handful of the negative cases are grammatically well-formed
// templates that fail only when expanded, and those are the carve-outs below.

// uriTemplateReason documents why a corpus case is expected to disagree with the
// uri-template validator. Every carve-out is a case where the corpus asserts an
// expansion outcome the grammar does not decide.
type uriTemplateReason string

const (
	reasonOpReserve uriTemplateReason = `RFC 6570 §2.2 operator = op-level2 / op-level3 / op-reserve, and op-reserve = "=" / "," / "!" / "@" / "|", so the template is ABNF-valid; the operator is reserved from expansion, not from the grammar`

	reasonPrefixOnComposite uriTemplateReason = `RFC 6570 §2.4.1 makes a prefix modifier an error only when the variable's *value* is composite, which a string-format check cannot see; the varspec itself is ABNF-valid`
)

var (
	// Corpus templates the validator classifies differently from the corpus,
	// each mapped to the reason. Keyed by template text, since a case carries
	// no identifier of its own.
	uriTemplateCarveOuts = map[string]uriTemplateReason{
		"{!hello}": reasonOpReserve,
		"{=path}":  reasonOpReserve,
		"{|var*}":  reasonOpReserve,

		"{keys:1}":  reasonPrefixOnComposite,
		"{+keys:1}": reasonPrefixOnComposite,
	}

	// Each vendored corpus file mapped to the validity its cases assert.
	uriTemplateCorpusFiles = map[string]bool{
		"spec-examples.json":            true,
		"spec-examples-by-section.json": true,
		"extended-tests.json":           true,
		"negative-tests.json":           false,
	}
)

// uriTemplateCase is one corpus template with its expected validity. The name
// carries the file, group, and index it came from, so two groups holding the
// same template stay distinguishable as subtests.
type uriTemplateCase struct {
	name     string
	template string
	valid    bool
}

// TestURITemplateCorpus runs every template in the vendored uritemplate-test
// corpus through the uri-template validator.
func TestURITemplateCorpus(t *testing.T) {
	t.Parallel()

	validate := validator(t, "uri-template")

	cases := loadURITemplateCorpus(t)
	require.Len(t, cases, 270, "corpus case count changed; refresh the counts in the corpus README")

	// Liveness: a carve-out that no longer names a live corpus case would sit
	// unexercised and mask a regression after a corpus refresh.
	present := make(map[string]bool, len(cases))
	for _, tc := range cases {
		present[tc.template] = true
	}

	for template := range uriTemplateCarveOuts {
		require.True(t, present[template],
			"carve-out %q is no longer in the corpus; drop it", template)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validate(tc.template)

			if reason, carved := uriTemplateCarveOuts[tc.template]; carved {
				require.NoError(t, err,
					"carved-out template %q must still be accepted as ABNF-valid: %s", tc.template, reason)

				return
			}

			if tc.valid {
				require.NoError(t, err, "corpus template %q should be a valid uri-template", tc.template)
			} else {
				require.Error(t, err, "corpus template %q should be an invalid uri-template", tc.template)
			}
		})
	}
}

// loadURITemplateCorpus decodes every vendored corpus file into a flat case
// list. The corpus shape is {group: {"variables": {...}, "testcases": [[template,
// expansion], ...]}}; the expansion element is decoded as json.RawMessage
// because it is a string in most cases, an array of acceptable expansions in
// others, and the boolean false throughout negative-tests.json.
func loadURITemplateCorpus(t *testing.T) []uriTemplateCase {
	t.Helper()

	var cases []uriTemplateCase

	for file, valid := range uriTemplateCorpusFiles {
		path := filepath.Join("testdata", "corpus", "uritemplate", file)

		data, err := os.ReadFile(path)
		require.NoError(t, err, "read corpus file %s", path)

		var groups map[string]struct {
			TestCases [][]json.RawMessage `json:"testcases"`
		}

		require.NoError(t, json.Unmarshal(data, &groups), "decode corpus file %s", path)

		for group, g := range groups {
			for i, tc := range g.TestCases {
				require.NotEmpty(t, tc, "%s/%s case %d has no template element", file, group, i)

				var template string

				require.NoError(t, json.Unmarshal(tc[0], &template),
					"%s/%s case %d template is not a string", file, group, i)

				cases = append(cases, uriTemplateCase{
					name:     fmt.Sprintf("%s/%s/%d/%s", file, group, i, template),
					template: template,
					valid:    valid,
				})
			}
		}
	}

	return cases
}
