package regexcache_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/regexcache"
)

func TestCompile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		pattern string
		err     bool
	}{
		"valid":   {pattern: `^a+b$`},
		"empty":   {pattern: ``},
		"invalid": {pattern: `a(`, err: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			re, err := regexcache.Compile(tc.pattern)
			if tc.err {
				require.Error(t, err)
				assert.Nil(t, re)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, re)
		})
	}
}

func TestCompileCachesCompiledExpression(t *testing.T) {
	t.Parallel()

	// Compiling the same valid pattern twice returns the identical cached
	// pointer, so each pattern compiles at most once.
	const pattern = `^cache-me-[0-9]+$`

	first, err := regexcache.Compile(pattern)
	require.NoError(t, err)

	second, err := regexcache.Compile(pattern)
	require.NoError(t, err)

	assert.Same(t, first, second)
}

func TestCompileCachesErrorFailClosed(t *testing.T) {
	t.Parallel()

	// An invalid pattern fails closed and returns the identical cached error on
	// every call, so the behavior is stable across runs rather than recompiling.
	const pattern = `fail-closed-(`

	_, first := regexcache.Compile(pattern)
	require.Error(t, first)

	_, second := regexcache.Compile(pattern)
	require.Error(t, second)

	assert.Same(t, first, second)
}
