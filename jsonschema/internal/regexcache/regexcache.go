// Package regexcache compiles regular-expression patterns once and shares the
// outcome across the whole process.
//
// A schema may reference the same pattern from many places, and a pattern
// reached only at validation time (through a remote or JSON-pointer fallback
// schema) would otherwise recompile on every run. Memoizing the outcome --
// including a compile error, so a pattern Go's RE2 engine rejects fails closed
// the same way every time -- keeps each distinct pattern at one compilation.
package regexcache

import (
	"fmt"
	"regexp"
	"sync"
)

// cache holds the memoized outcome of compiling each pattern, keyed by pattern
// string.
var cache sync.Map

// cached is the memoized result of compiling one pattern.
type cached struct {
	re  *regexp.Regexp
	err error
}

// Compile compiles pattern with Go's RE2 engine, returning the same compiled
// expression or compile error for every call with a given pattern. The cached
// error is shared across calls; callers only test it for non-nil and never
// mutate the returned expression.
func Compile(pattern string) (*regexp.Regexp, error) {
	if v, ok := cache.Load(pattern); ok {
		if c, ok := v.(cached); ok {
			return c.re, c.err
		}
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		err = fmt.Errorf("compile regexp: %w", err)
	}

	// Cache the outcome including failures, so an invalid pattern reached
	// through the validation-time fallback (a remote/uncached schema) compiles
	// at most once. The cached error is shared across runs; callers only test it
	// for non-nil and never mutate it.
	cache.Store(pattern, cached{re: re, err: err})

	return re, err
}
