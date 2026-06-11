package cobras

import (
	"github.com/spf13/cobra"
)

// Must panics if err is non-nil. It is intended for cobra/pflag setup calls
// that return errors only on programmer mistakes, such as
// [github.com/spf13/cobra.Command.MarkFlagRequired] with a misspelled flag
// name, or completion registration during command construction.
func Must(err error) {
	if err != nil {
		panic(err)
	}
}

// MustMarkFlagsRequired marks each named flag as required on cmd, panicking
// on programmer error such as a misspelled flag name.
func MustMarkFlagsRequired(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		Must(cmd.MarkFlagRequired(name))
	}
}

// ChainPersistentPreRunE sets cmd.PersistentPreRunE to call f after the hook
// it replaces. If cmd has no hook of its own (neither PersistentPreRunE nor
// PersistentPreRun), the nearest parent's persistent pre-run hook is invoked
// first, resolved at execution time so commands may be wired before
// [github.com/spf13/cobra.Command.AddCommand] attaches parents. This
// preserves setup that cobra's nearest-hook traversal would otherwise skip.
func ChainPersistentPreRunE(cmd *cobra.Command, f func(*cobra.Command, []string) error) {
	prev := cmd.PersistentPreRunE
	prevPlain := cmd.PersistentPreRun

	cmd.PersistentPreRunE = func(cc *cobra.Command, args []string) error {
		if prev != nil {
			err := prev(cc, args)
			if err != nil {
				return err
			}

			return f(cc, args)
		}

		// The command's own non-error hook was the nearest hook before
		// chaining; the new PersistentPreRunE shadows it in cobra's
		// dispatch, so run it here to preserve nearest-hook semantics.
		if prevPlain != nil {
			prevPlain(cc, args)

			return f(cc, args)
		}

		// Walk parents of cmd, the command the hook was installed on; cobra
		// passes the executed subcommand as cc, and walking from cc would
		// re-enter this closure.
		for parent := cmd.Parent(); parent != nil; parent = parent.Parent() {
			if parent.PersistentPreRunE != nil {
				err := parent.PersistentPreRunE(cc, args)
				if err != nil {
					return err //nolint:wrapcheck // Propagates the parent hook's error verbatim.
				}

				break
			}

			if parent.PersistentPreRun != nil {
				parent.PersistentPreRun(cc, args)

				break
			}
		}

		return f(cc, args)
	}
}
