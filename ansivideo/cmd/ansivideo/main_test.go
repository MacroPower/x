package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunReturnsUsageExitCode(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		args []string
	}{
		"no arguments":   {args: []string{}},
		"too many files": {args: []string{"a.mp4", "b.mp4"}},
		"unknown flag":   {args: []string{"-nope", "a.mp4"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code := run(tc.args)

			assert.Equal(t, 2, code)
		})
	}
}

func TestRunVersionFlagExitsZero(t *testing.T) {
	t.Parallel()

	// -version prints build info and exits successfully without requiring a
	// video file argument.
	code := run([]string{"-version"})

	assert.Equal(t, 0, code)
}

func TestTerminalSizeFromWidth(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		width    int
		wantCols int
		wantRows int
	}{
		"wide":   {width: 80, wantCols: 80, wantRows: 22},
		"narrow": {width: 16, wantCols: 16, wantRows: 4},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cols, rows, err := terminalSize(tc.width)

			require.NoError(t, err)
			assert.Equal(t, tc.wantCols, cols)
			assert.Equal(t, tc.wantRows, rows)
		})
	}
}
