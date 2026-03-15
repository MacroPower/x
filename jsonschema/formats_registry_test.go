//nolint:testpackage // white-box: needs access to the unexported builtinFormats map.
package jsonschema

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBuiltinFormatsRegistered asserts the exact set of built-in format
// checkers, so adding or removing one is a deliberate, reviewed change.
func TestBuiltinFormatsRegistered(t *testing.T) {
	t.Parallel()

	want := []string{
		"date",
		"date-time",
		"duration",
		"email",
		"hostname",
		"idn-email",
		"idn-hostname",
		"ipv4",
		"ipv6",
		"iri",
		"iri-reference",
		"json-pointer",
		"regex",
		"relative-json-pointer",
		"time",
		"uri",
		"uri-reference",
		"uri-template",
		"uuid",
	}

	got := make([]string, 0, len(builtinFormats))
	for name := range builtinFormats {
		got = append(got, name)
	}

	sort.Strings(got)
	sort.Strings(want)

	assert.Equal(t, want, got)
}
