// Package regexcache compiles regular-expression patterns once and shares the
// outcome across the whole process.
//
// A schema may reference the same pattern from many places, and a pattern
// reached only at validation time (through a remote or JSON-pointer fallback
// schema) would otherwise recompile on every run. Memoizing the outcome --
// including a compile error, so a pattern Go's RE2 engine rejects fails closed
// the same way every time -- keeps each distinct pattern at one compilation.
//
// The cache holds at most [MaxEntries] distinct patterns. Reaching the cap
// clears it, so a process compiling an unbounded stream of caller-supplied
// patterns holds a bounded number of compiled expressions rather than one per
// pattern it ever saw; the patterns still in use compile again on their next
// call.
package regexcache

import (
	"fmt"
	"regexp"
	"sync"
)

// MaxEntries is the number of distinct patterns the cache holds before it
// clears itself.
const MaxEntries = 4096

// cache holds the memoized outcome of compiling each pattern, keyed by pattern
// string, under mu.
var (
	mu    sync.Mutex
	cache = map[string]cached{}
)

// cached is the memoized result of compiling one pattern.
type cached struct {
	re  *regexp.Regexp
	err error
}

// Compile compiles pattern with Go's RE2 engine, returning the same compiled
// expression or compile error for every call with a given pattern while the
// entry is cached. The cached error is shared across calls; callers only test
// it for non-nil and never mutate the returned expression.
func Compile(pattern string) (*regexp.Regexp, error) {
	mu.Lock()

	c, ok := cache[pattern]

	mu.Unlock()

	if ok {
		return c.re, c.err
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		err = fmt.Errorf("compile regexp: %w", err)
	}

	// Cache the outcome including failures, so an invalid pattern reached
	// through the validation-time fallback (a remote/uncached schema) compiles
	// at most once. When two goroutines race the first compile of one pattern,
	// the loser discards its own result and both return the winner's, so every
	// call for a given pattern observes the same compiled expression (and
	// error) while the entry lives.
	mu.Lock()
	defer mu.Unlock()

	if c, ok := cache[pattern]; ok {
		return c.re, c.err
	}

	if len(cache) >= MaxEntries {
		clear(cache)
	}

	cache[pattern] = cached{re: re, err: err}

	return re, err
}

// size reports the number of cached patterns, for the bound test.
func size() int {
	mu.Lock()
	defer mu.Unlock()

	return len(cache)
}
