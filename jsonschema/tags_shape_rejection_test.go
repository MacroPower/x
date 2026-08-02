package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestTagKeywordShapeCannotCarryIsRejected pins that a jsonschema tag keyword
// the field's shape cannot carry is an error rather than a keyword nothing
// enforces. Emitting it would read as a constraint while constraining nothing,
// which is the failure mode the shared model exists to make unavailable.
func TestTagKeywordShapeCannotCarryIsRejected(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		build func() (*jsonschema.Schema, error)
		want  string
	}{
		"an item count on a string": {
			want: "cannot carry a item count bound",
			build: func() (*jsonschema.Schema, error) {
				type T struct {
					V string `json:"v" jsonschema:"minItems=3"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"a length on a number": {
			want: "cannot carry a length bound",
			build: func() (*jsonschema.Schema, error) {
				type T struct {
					V int `json:"v" jsonschema:"minLength=3"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
		"a numeric bound on a coerced field": {
			// The instance is a quoted string, which minimum cannot constrain,
			// and JSON Schema has no keyword for the magnitude of a string.
			want: "coerced numeric field",
			build: func() (*jsonschema.Schema, error) {
				type T struct {
					V int `json:"v,string" jsonschema:"minimum=5"`
				}

				return jsonschema.GenerateFor[T](t.Context())
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.build()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestTagCoercedScalarIsRangeChecked pins that a scalar on a field whose schema
// is a string because it serializes itself as one still parses at the real Go
// kind, so a value the field could never hold is an error rather than a const
// pinned to text the field never writes.
func TestTagCoercedScalarIsRangeChecked(t *testing.T) {
	t.Parallel()

	type T struct {
		V int8 `json:"v,string" jsonschema:"const=200"`
	}

	_, err := jsonschema.GenerateFor[T](t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

// TestTagRepeatedBoundIntersects pins that repeating a bound key in one tag
// composes the two rather than letting the last one win, matching how bounds
// from every other source merge.
func TestTagRepeatedBoundIntersects(t *testing.T) {
	t.Parallel()

	type T struct {
		V int `json:"v" jsonschema:"minimum=5,minimum=3"`
	}

	s, err := jsonschema.GenerateFor[T](t.Context())
	require.NoError(t, err)

	field := s.Properties["v"]
	require.NotNil(t, field.Minimum)
	assert.InDelta(t, 5.0, *field.Minimum, 0,
		"the weaker second floor does not lower the first")
}

// TestTagRepeatedStringKeywordConflicts pins that naming a string keyword twice
// in one tag is an error. Across sources there is a documented precedence to
// appeal to; within one tag there is none, so silently dropping one of two
// stated values would be worse than reporting.
func TestTagRepeatedStringKeywordConflicts(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func() (*jsonschema.Schema, error){
		"pattern": func() (*jsonschema.Schema, error) {
			type T struct {
				V string `json:"v" jsonschema:"pattern=^a$,pattern=^b$"`
			}

			return jsonschema.GenerateFor[T](t.Context())
		},
		"format": func() (*jsonschema.Schema, error) {
			type T struct {
				V string `json:"v" jsonschema:"format=email,format=uuid"`
			}

			return jsonschema.GenerateFor[T](t.Context())
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := build()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is set twice in one tag")
		})
	}
}

// TestTagSecondConstConflicts pins that a second const, or one disagreeing with
// a value the field's type already pins, is a conflict rather than an overwrite:
// both fully describe the allowed value, so neither can silently win.
func TestTagSecondConstConflicts(t *testing.T) {
	t.Parallel()

	type T struct {
		V int `json:"v" jsonschema:"const=3,const=9"`
	}

	_, err := jsonschema.GenerateFor[T](t.Context())
	require.Error(t, err)
	require.ErrorIs(t, err, jsonschema.ErrConstraintConflict)
}

// TestTagDefaultUsesTheSerializedForm pins that default parses against the
// field's schema shape like const does, so a field whose schema is a string
// because it serializes itself as one gets a default it can actually hold.
func TestTagDefaultUsesTheSerializedForm(t *testing.T) {
	t.Parallel()

	type T struct {
		V int `json:"v,string" jsonschema:"default=3"`
	}

	s, err := jsonschema.GenerateFor[T](t.Context())
	require.NoError(t, err)

	assert.JSONEq(t, `"3"`, string(s.Properties["v"].Default),
		"the default is the text the field emits, not the bare number")
}
