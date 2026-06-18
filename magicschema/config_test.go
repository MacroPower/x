package magicschema_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm"
	"go.jacobcolvin.com/x/magicschema/helm/dadav"
	"go.jacobcolvin.com/x/magicschema/helm/losisin"
	"go.jacobcolvin.com/x/magicschema/helm/norwoodj"
)

func TestRegistryLookup(t *testing.T) {
	t.Parallel()

	registry := helm.DefaultRegistry()

	tcs := map[string]struct {
		names []string
		want  []string
		err   error
	}{
		"resolves names preserving the given order": {
			names: []string{norwoodj.Name, dadav.Name},
			want:  []string{norwoodj.Name, dadav.Name},
		},
		"zero names yield an empty list": {
			names: nil,
			want:  []string{},
		},
		"unknown name": {
			names: []string{dadav.Name, "nonexistent"},
			err:   magicschema.ErrUnknownAnnotator,
		},
		"exact match only, no trimming": {
			names: []string{" " + dadav.Name},
			err:   magicschema.ErrUnknownAnnotator,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := registry.Lookup(tc.names...)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				assert.Nil(t, got)

				return
			}

			require.NoError(t, err)

			gotNames := make([]string, 0, len(got))
			for _, a := range got {
				gotNames = append(gotNames, a.Name())
			}

			assert.Equal(t, tc.want, gotNames)
		})
	}
}

func TestRegistryNames(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		annotators []magicschema.Annotator
		want       []string
	}{
		"sorted regardless of registration order": {
			annotators: []magicschema.Annotator{losisin.New(), dadav.New()},
			want:       []string{dadav.Name, losisin.Name},
		},
		"empty registry": {
			annotators: nil,
			want:       nil,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			registry := make(magicschema.Registry)
			registry.Add(tc.annotators...)

			assert.Equal(t, tc.want, registry.Names())
		})
	}
}

func TestConfigNewGeneratorDraft(t *testing.T) {
	t.Parallel()

	t.Run("NewConfig defaults to the supported draft", func(t *testing.T) {
		t.Parallel()

		cfg := magicschema.NewConfig()
		assert.Equal(t, 7, cfg.Draft)

		gen, err := cfg.NewGenerator()
		require.NoError(t, err)
		assert.NotNil(t, gen)
	})

	t.Run("explicit unsupported draft is rejected", func(t *testing.T) {
		t.Parallel()

		for _, draft := range []int{0, 4, 2020} {
			cfg := magicschema.NewConfig()
			cfg.Draft = draft

			_, err := cfg.NewGenerator()
			require.ErrorIs(t, err, magicschema.ErrInvalidOption,
				"draft %d must be rejected", draft)
		}
	})
}

func TestConfigMustRegisterCompletions(t *testing.T) {
	t.Parallel()

	t.Run("registered flags do not panic", func(t *testing.T) {
		t.Parallel()

		cfg := magicschema.NewConfig()
		cmd := &cobra.Command{Use: "test"}
		cfg.RegisterFlags(cmd.Flags())

		require.NotPanics(t, func() { cfg.MustRegisterCompletions(cmd) })
	})

	t.Run("missing flags panic", func(t *testing.T) {
		t.Parallel()

		cfg := magicschema.NewConfig()
		cmd := &cobra.Command{Use: "test"}

		require.Panics(t, func() { cfg.MustRegisterCompletions(cmd) })
	})
}

func TestConfigAnnotatorsCompletion(t *testing.T) {
	t.Parallel()

	cfg := magicschema.NewConfig()
	cfg.Registry = helm.DefaultRegistry()

	rootCmd := &cobra.Command{Use: "magicschema", Run: func(*cobra.Command, []string) {}}
	cfg.RegisterFlags(rootCmd.Flags())
	require.NoError(t, cfg.RegisterCompletions(rootCmd))

	var buf bytes.Buffer

	rootCmd.SetOut(&buf)
	rootCmd.SetArgs([]string{cobra.ShellCompNoDescRequestCmd, "--annotators", "helm-schema,"})
	require.NoError(t, rootCmd.Execute())

	var candidates []string

	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		candidates = append(candidates, strings.SplitN(line, "\t", 2)[0])
	}

	// The already-typed "helm-schema," prefix is preserved and the next
	// name is appended; helm-schema itself is filtered out.
	assert.Contains(t, candidates, "helm-schema,bitnami")
	assert.NotContains(t, candidates, "helm-schema,helm-schema")
}
