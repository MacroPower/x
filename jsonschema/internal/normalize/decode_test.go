package normalize_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
)

func TestDecodeJSONInstance(t *testing.T) {
	t.Parallel()

	t.Run("uses json.Number", func(t *testing.T) {
		t.Parallel()

		got, err := normalize.DecodeJSONInstance([]byte(`5`))
		require.NoError(t, err)
		assert.Equal(t, json.Number("5"), got)
	})

	t.Run("trailing whitespace is accepted", func(t *testing.T) {
		t.Parallel()

		got, err := normalize.DecodeJSONInstance([]byte("\"x\"  \n"))
		require.NoError(t, err)
		assert.Equal(t, "x", got)
	})

	t.Run("trailing data is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := normalize.DecodeJSONInstance([]byte(`true false`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected data after top-level value")
	})

	t.Run("invalid json is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := normalize.DecodeJSONInstance([]byte(`{`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JSON decode:")
	})
}
