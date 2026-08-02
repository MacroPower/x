package tagmodel

// This file exposes the internals the exhaustiveness tests walk. They live here
// rather than in the package proper so the production surface stays the model
// itself: a front-end has no reason to enumerate forms or resolve an axis by
// hand, and giving it the means would invite exactly the re-derivation the
// package exists to remove.

// Forms returns every classified form, so a test can walk the matrix columns
// without hardcoding the list and silently missing a new one.
func Forms() []Form {
	out := make([]Form, 0, formCount-1)
	for f := FormUnset + 1; f < formCount; f++ {
		out = append(out, f)
	}

	return out
}

// Axes returns every axis, including AxisAuto, which a bound resolves through
// rather than lands on.
func Axes() []Axis {
	out := make([]Axis, 0, axisCount)
	for a := range int(axisCount) {
		out = append(out, Axis(a))
	}

	return out
}

// ResolveAxisForTest exposes the axis resolution so the compatibility test can
// assert every pair either resolves or reports a reason.
func ResolveAxisForTest(form Form, axis Axis) (Axis, error) { return resolveAxis(form, axis) }

// ZeroLiteralForTest exposes the non-zero comparison literal, so the scalar
// tests can pin that a coerced shape forbids the text it actually emits.
func (sh Shape) ZeroLiteralForTest() string { return sh.zeroLiteral() }
