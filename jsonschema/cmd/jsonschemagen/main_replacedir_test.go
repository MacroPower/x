package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckReplaceDirFor(t *testing.T) {
	t.Parallel()

	// The modfile parser rejects a backslash in a replacement path only on a
	// non-Windows system (its rule is gated on filepath.Separator == '/'). On
	// Windows every directory contains backslashes and the go tool accepts
	// them in a replace directive, so the pre-check must mirror the same gate
	// or the tool rejects every invocation on a Windows host.
	tests := map[string]struct {
		dir       string
		separator rune
		err       string
	}{
		"plain path on slash host": {
			dir:       "/tmp/mymod",
			separator: '/',
		},
		"backslash on slash host rejected": {
			dir:       `/tmp/weird\dir`,
			separator: '/',
			err:       "backslash",
		},
		"windows path on windows host accepted": {
			dir:       `C:\Users\me\proj`,
			separator: '\\',
		},
		"windows path with spaces on windows host accepted": {
			dir:       `C:\Users\my user\proj`,
			separator: '\\',
		},
		"empty dir on windows host rejected": {
			dir:       "",
			separator: '\\',
			err:       "reported no local directory",
		},
		"empty dir on slash host rejected": {
			dir:       "",
			separator: '/',
			err:       "reported no local directory",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := checkReplaceDirFor("module directory", tc.dir, tc.separator)
			if tc.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}
