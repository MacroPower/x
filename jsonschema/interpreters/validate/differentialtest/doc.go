// Package differentialtest houses the go-playground/validator differential rig
// for the validate interpreter. It is a separate, test-only nested module so
// the oracle library and its transitive dependencies never enter the published
// jsonschema module's requirement graph; the workspace (go.work) wires it into
// the same lint, tidy, and test tasks as every other module.
package differentialtest
