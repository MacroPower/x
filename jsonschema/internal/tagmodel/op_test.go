package tagmodel_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

// TestRuleValidate pins the whole check a hand-built rule owes the model:
// the operation and axis index their tables, and the parameters suit the
// operation, so an applier never reads a count it would misread.
func TestRuleValidate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		rule tagmodel.Rule
		err  string
	}{
		"bound": {rule: tagmodel.Rule{Op: tagmodel.OpFloorIncl, Params: tagmodel.ParamsOf("1")}},
		"pinned axis": {
			rule: tagmodel.Rule{Op: tagmodel.OpCeilIncl, Axis: tagmodel.AxisLength, Params: tagmodel.ParamsOf("3")},
		},
		"enumeration": {rule: tagmodel.Rule{Op: tagmodel.OpOneOf, Params: tagmodel.ParamsOf("a", "b")}},
		"non-zero":    {rule: tagmodel.Rule{Op: tagmodel.OpNonZero}},
		"unique":      {rule: tagmodel.Rule{Op: tagmodel.OpUnique, Params: tagmodel.ParamsOf("true")}},
		"unset op":    {rule: tagmodel.Rule{Params: tagmodel.ParamsOf("1")}, err: "unset"},
		"op past the table": {
			rule: tagmodel.Rule{Op: tagmodel.Op(200), Params: tagmodel.ParamsOf("1")},
			err:  "Op(200)",
		},
		"axis past the table": {
			rule: tagmodel.Rule{Op: tagmodel.OpFloorIncl, Axis: tagmodel.Axis(9), Params: tagmodel.ParamsOf("1")},
			err:  "Axis(9)",
		},
		"missing value": {rule: tagmodel.Rule{Op: tagmodel.OpEqual}, err: "expects exactly one value, got 0"},
		"extra value": {
			rule: tagmodel.Rule{Op: tagmodel.OpEqual, Params: tagmodel.ParamsOf("a", "b")},
			err:  "got 2",
		},
		"empty enumeration": {rule: tagmodel.Rule{Op: tagmodel.OpOneOf}, err: "at least one value"},
		"unique non-boolean": {
			rule: tagmodel.Rule{Op: tagmodel.OpUnique, Params: tagmodel.ParamsOf("yes")},
			err:  "yes",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.rule.Validate()
			if tc.err == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tc.err)
		})
	}
}
