package main

import "dagger/xci/internal/dagger"

// Format runs golangci-lint --fix and prettier --write, returning the merged
// changeset against the original source directory.
//
// Both formatters operate on non-overlapping file types (.go vs
// .yaml/.md/.json), so they run against the original source in parallel and
// their changesets merge without conflicts.
//
// +generate
func (m *Xci) Format() *dagger.Changeset {
	return m.Go.FormatGo().WithChangeset(m.Prettier.Format())
}
