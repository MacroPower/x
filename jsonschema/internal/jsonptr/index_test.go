package jsonptr_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

func TestParseArrayIndex(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		seg     string
		wantIdx int
		wantOK  bool
	}{
		"zero":            {seg: "0", wantIdx: 0, wantOK: true},
		"positive":        {seg: "10", wantIdx: 10, wantOK: true},
		"leading zero":    {seg: "01", wantOK: false},
		"plus sign":       {seg: "+1", wantOK: false},
		"negative zero":   {seg: "-0", wantOK: false},
		"negative":        {seg: "-1", wantOK: false},
		"empty":           {seg: "", wantOK: false},
		"non-digit":       {seg: "a", wantOK: false},
		"mixed":           {seg: "1a", wantOK: false},
		"overflow length": {seg: "99999999999999999999999999", wantOK: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			idx, ok := jsonptr.ParseArrayIndex(tc.seg)
			assert.Equal(t, tc.wantOK, ok)

			if tc.wantOK {
				assert.Equal(t, tc.wantIdx, idx)
			}
		})
	}
}
