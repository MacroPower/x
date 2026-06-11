// Package cobras collects helpers for building CLI applications with
// [github.com/spf13/cobra] and [github.com/spf13/pflag].
//
// The root package provides small command-wiring helpers: [Must] and
// [MustMarkFlagsRequired] for setup calls that only return errors on
// programmer mistakes, and [ChainPersistentPreRunE] for composing persistent
// pre-run hooks that cobra's nearest-hook traversal would otherwise skip.
//
// Each subpackage stands on its own:
//
//   - [go.jacobcolvin.com/x/cobras/log] builds [log/slog] handlers with flag
//     and completion integration.
//   - [go.jacobcolvin.com/x/cobras/profile] manages runtime profiling for the
//     lifetime of a command.
package cobras
