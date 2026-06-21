package regexcache_test

import (
	"regexp"
	"sync"
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

func TestCompileFirstCompileRaceReturnsSamePointer(t *testing.T) {
	t.Parallel()

	// Race the first compile of a fresh pattern across many goroutines. The
	// compile-and-cache is atomic, so every goroutine observes the same
	// *regexp.Regexp; a non-atomic store would hand the losers their own
	// locally-compiled pointers. Run under -race, this also catches the data
	// race on the cache.
	const (
		pattern = `^race-me-[0-9]+$`
		n       = 64
	)

	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		results = make([]*regexp.Regexp, n)
	)

	wg.Add(n)

	for i := range n {
		go func() {
			defer wg.Done()

			<-start

			re, err := regexcache.Compile(pattern)
			assert.NoError(t, err)

			results[i] = re
		}()
	}

	close(start)
	wg.Wait()

	for i := 1; i < n; i++ {
		assert.Same(t, results[0], results[i])
	}
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
