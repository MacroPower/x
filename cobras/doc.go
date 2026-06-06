// Package cobras collects helpers for building CLI applications with
// [github.com/spf13/cobra] and [github.com/spf13/pflag].
//
// Each subpackage stands on its own:
//
//   - [go.jacobcolvin.com/x/cobras/log] builds [log/slog] handlers with flag
//     and completion integration.
//   - [go.jacobcolvin.com/x/cobras/profile] manages runtime profiling for the
//     lifetime of a command.
//   - [go.jacobcolvin.com/x/cobras/version] exposes build version information
//     from ldflags and build info.
package cobras
