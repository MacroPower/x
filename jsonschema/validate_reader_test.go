package jsonschema_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// validateReader compiles schema and validates the reader's JSON against it,
// the Compile-then-ValidateReader composition, transparent like validateJSON.
//
//nolint:wrapcheck // Transparent test helper; assertions match the original errors.
func validateReader(
	ctx context.Context, schema *jsonschema.Schema, r io.Reader, opts ...jsonschema.ValidateOption,
) error {
	v, err := jsonschema.Compile(ctx, schema, opts...)
	if err != nil {
		return err
	}

	return v.ValidateReader(ctx, r)
}

func TestValidateReader(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		json   string
		err    string
		isVE   bool
	}{
		"valid object": {
			schema: &jsonschema.Schema{
				Type:       "object",
				Required:   []string{"name"},
				Properties: map[string]*jsonschema.Schema{"name": {Type: "string"}},
			},
			json: `{"name": "Alice"}`,
		},
		"validation failure": {
			schema: &jsonschema.Schema{Type: "string"},
			json:   `1`,
			err:    "(type)",
			isVE:   true,
		},
		"malformed json": {
			schema: &jsonschema.Schema{Type: "object"},
			json:   `{invalid`,
			err:    "JSON decode",
		},
		"trailing data": {
			schema: &jsonschema.Schema{Type: "object"},
			json:   `{"a":1} x`,
			err:    "JSON decode",
		},
		"trailing whitespace accepted": {
			schema: &jsonschema.Schema{Type: "object"},
			json:   "{\"a\":1}\n  \t\n",
		},
		"duplicate member names rejected": {
			schema: &jsonschema.Schema{Type: "object"},
			json:   `{"a":1,"a":2}`,
			err:    "JSON decode",
		},
		"integer preserved as jsonv1.Number": {
			schema: &jsonschema.Schema{Type: "integer"},
			json:   `42`,
		},
		"float rejected by integer type": {
			schema: &jsonschema.Schema{Type: "integer"},
			json:   `3.14`,
			err:    "(type)",
			isVE:   true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validateReader(t.Context(), tt.schema, strings.NewReader(tt.json))
			if tt.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)

			ve, isVE := errors.AsType[*jsonschema.ValidationError](err)
			assert.Equal(t, tt.isVE, isVE)

			if tt.isVE {
				require.NotNil(t, ve)
			}
		})
	}

	t.Run("reader error mid-stream", func(t *testing.T) {
		t.Parallel()

		errBoom := errors.New("boom")
		r := io.MultiReader(strings.NewReader(`{"a":`), iotest.ErrReader(errBoom))

		err := validateReader(t.Context(), &jsonschema.Schema{Type: "object"}, r)
		require.Error(t, err)
		require.ErrorIs(t, err, errBoom)

		_, isVE := errors.AsType[*jsonschema.ValidationError](err)
		assert.False(t, isVE, "a read error carries no validation verdict")
	})

	t.Run("agrees with ValidateJSON", func(t *testing.T) {
		t.Parallel()

		schema := &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{"n": {Type: "integer"}},
		}

		v, err := jsonschema.Compile(t.Context(), schema)
		require.NoError(t, err)

		for _, data := range []string{`{"n": 1}`, `{"n": 1.5}`, `{"n": 1} x`, `{`} {
			fromBytes := v.ValidateJSON(t.Context(), []byte(data))
			fromReader := v.ValidateReader(t.Context(), bytes.NewReader([]byte(data)))

			if fromBytes == nil {
				assert.NoError(t, fromReader, "input %q", data)
			} else {
				assert.Error(t, fromReader, "input %q", data)
			}
		}
	})
}
