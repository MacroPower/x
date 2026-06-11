package cobras_test

import (
	"errors"
	"io"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/cobras"
)

func TestMust(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		err    error
		panics bool
	}{
		"nil error does not panic": {
			err:    nil,
			panics: false,
		},
		"non-nil error panics": {
			err:    errors.New("boom"),
			panics: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if tc.panics {
				require.Panics(t, func() { cobras.Must(tc.err) })
			} else {
				require.NotPanics(t, func() { cobras.Must(tc.err) })
			}
		})
	}
}

func TestMustMarkFlagsRequired(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		names  []string
		panics bool
	}{
		"existing flags": {
			names:  []string{"alpha", "beta"},
			panics: false,
		},
		"no names": {
			names:  nil,
			panics: false,
		},
		"misspelled flag panics": {
			names:  []string{"alpha", "nope"},
			panics: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd := &cobra.Command{Use: "test"}
			cmd.Flags().String("alpha", "", "first flag")
			cmd.Flags().String("beta", "", "second flag")

			markFn := func() { cobras.MustMarkFlagsRequired(cmd, tc.names...) }

			if tc.panics {
				require.Panics(t, markFn)

				return
			}

			require.NotPanics(t, markFn)

			for _, flagName := range tc.names {
				flag := cmd.Flags().Lookup(flagName)
				require.NotNil(t, flag)
				assert.Equal(t, []string{"true"}, flag.Annotations[cobra.BashCompOneRequiredFlag])
			}
		})
	}
}

func TestChainPersistentPreRunE(t *testing.T) {
	t.Parallel()

	errParent := errors.New("parent hook")
	errChained := errors.New("chained hook")

	tcs := map[string]struct {
		build func(record func(string)) *cobra.Command
		want  []string
		err   error
	}{
		"no parent runs f alone": {
			build: func(record func(string)) *cobra.Command {
				cmd := &cobra.Command{Use: "child"}
				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f", nil))

				return cmd
			},
			want: []string{"f"},
		},
		"parent hook runs first": {
			build: func(record func(string)) *cobra.Command {
				parent := &cobra.Command{
					Use:               "parent",
					PersistentPreRunE: recordHook(record, "parent", nil),
				}
				cmd := &cobra.Command{Use: "child"}

				// Chain before AddCommand: the parent is resolved at
				// execution time.
				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f", nil))
				parent.AddCommand(cmd)

				return cmd
			},
			want: []string{"parent", "f"},
		},
		"grandparent hook found through hookless parent": {
			build: func(record func(string)) *cobra.Command {
				grandparent := &cobra.Command{
					Use:               "grandparent",
					PersistentPreRunE: recordHook(record, "grandparent", nil),
				}
				parent := &cobra.Command{Use: "parent"}
				cmd := &cobra.Command{Use: "child"}

				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f", nil))
				grandparent.AddCommand(parent)
				parent.AddCommand(cmd)

				return cmd
			},
			want: []string{"grandparent", "f"},
		},
		"parent non-error hook runs first": {
			build: func(record func(string)) *cobra.Command {
				parent := &cobra.Command{
					Use: "parent",
					PersistentPreRun: func(_ *cobra.Command, _ []string) {
						record("parent")
					},
				}
				cmd := &cobra.Command{Use: "child"}

				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f", nil))
				parent.AddCommand(cmd)

				return cmd
			},
			want: []string{"parent", "f"},
		},
		"command's own non-error hook runs first, parent skipped": {
			build: func(record func(string)) *cobra.Command {
				parent := &cobra.Command{
					Use:               "parent",
					PersistentPreRunE: recordHook(record, "parent", nil),
				}
				cmd := &cobra.Command{
					Use: "child",
					PersistentPreRun: func(_ *cobra.Command, _ []string) {
						record("child-own")
					},
				}

				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f", nil))
				parent.AddCommand(cmd)

				return cmd
			},
			// The child's own hook was the nearest hook before chaining, so
			// it still runs and the parent stays skipped, matching cobra's
			// nearest-hook dispatch.
			want: []string{"child-own", "f"},
		},
		"chaining twice runs both after parent runs once": {
			build: func(record func(string)) *cobra.Command {
				parent := &cobra.Command{
					Use:               "parent",
					PersistentPreRunE: recordHook(record, "parent", nil),
				}
				cmd := &cobra.Command{Use: "child"}

				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f1", nil))
				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f2", nil))
				parent.AddCommand(cmd)

				return cmd
			},
			want: []string{"parent", "f1", "f2"},
		},
		"parent error short-circuits f": {
			build: func(record func(string)) *cobra.Command {
				parent := &cobra.Command{
					Use:               "parent",
					PersistentPreRunE: recordHook(record, "parent", errParent),
				}
				cmd := &cobra.Command{Use: "child"}

				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f", nil))
				parent.AddCommand(cmd)

				return cmd
			},
			want: []string{"parent"},
			err:  errParent,
		},
		"f error propagates": {
			build: func(record func(string)) *cobra.Command {
				cmd := &cobra.Command{Use: "child"}
				cobras.ChainPersistentPreRunE(cmd, recordHook(record, "f", errChained))

				return cmd
			},
			want: []string{"f"},
			err:  errChained,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got []string

			record := func(step string) { got = append(got, step) }

			cmd := tc.build(record)
			err := cmd.PersistentPreRunE(cmd, nil)

			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// TestChainPersistentPreRunE_Execute runs a full cobra invocation of a leaf
// subcommand. Cobra passes the executed leaf as the hook argument, so this
// exercises the parent walk starting from the command the hook was installed
// on rather than from the leaf.
func TestChainPersistentPreRunE_Execute(t *testing.T) {
	t.Parallel()

	var got []string

	record := func(step string) { got = append(got, step) }

	root := &cobra.Command{
		Use:               "root",
		PersistentPreRunE: recordHook(record, "root", nil),
	}
	mid := &cobra.Command{Use: "mid"}
	leaf := &cobra.Command{
		Use:  "leaf",
		RunE: func(_ *cobra.Command, _ []string) error { return nil },
	}

	cobras.ChainPersistentPreRunE(mid, func(cc *cobra.Command, _ []string) error {
		record("f")
		assert.Same(t, leaf, cc)

		return nil
	})

	root.AddCommand(mid)
	mid.AddCommand(leaf)

	root.SetArgs([]string{"mid", "leaf"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	require.NoError(t, root.Execute())
	assert.Equal(t, []string{"root", "f"}, got)
}

// recordHook returns a persistent pre-run hook that records step and returns
// err.
func recordHook(record func(string), step string, err error) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		record(step)

		return err
	}
}
