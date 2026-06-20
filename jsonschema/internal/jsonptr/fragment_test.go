package jsonptr_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

func TestFragmentSegments(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		fragment string
		encoded  bool
		want     []string
		wantOK   bool
	}{
		"plain pointer splits on slash": {
			fragment: "/properties/name",
			want:     []string{"properties", "name"},
			wantOK:   true,
		},
		"root pointer yields one empty segment": {
			fragment: "/",
			want:     []string{""},
			wantOK:   true,
		},
		"missing root separator is rejected": {
			fragment: "properties/name",
			wantOK:   false,
		},
		"empty fragment is rejected": {
			fragment: "",
			wantOK:   false,
		},
		"~1 unescapes to slash within a token": {
			fragment: "/a~1b",
			want:     []string{"a/b"},
			wantOK:   true,
		},
		"~0 unescapes to tilde within a token": {
			fragment: "/a~0b",
			want:     []string{"a~b"},
			wantOK:   true,
		},
		"encoded %2F root separator is stripped": {
			fragment: "%2Fproperties%2Fname",
			encoded:  true,
			want:     []string{"properties", "name"},
			wantOK:   true,
		},
		"encoded lowercase %2f root separator is stripped": {
			fragment: "%2fproperties",
			encoded:  true,
			want:     []string{"properties"},
			wantOK:   true,
		},
		"encoded percent-escapes are decoded per token": {
			fragment: "/a%20b",
			encoded:  true,
			want:     []string{"a b"},
			wantOK:   true,
		},
		"unencoded fragment keeps literal percent": {
			fragment: "/a%20b",
			encoded:  false,
			want:     []string{"a%20b"},
			wantOK:   true,
		},
		"invalid percent-escape left as-is when encoded": {
			fragment: "/a%zzb",
			encoded:  true,
			want:     []string{"a%zzb"},
			wantOK:   true,
		},
		"encoded %2F inside a token splits like a separator": {
			fragment: "/foo%2Fbar",
			encoded:  true,
			want:     []string{"foo", "bar"},
			wantOK:   true,
		},
		"non-root-separator prefix is rejected even when encoded": {
			fragment: "foo",
			encoded:  true,
			wantOK:   false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := jsonptr.FragmentSegments(tt.fragment, tt.encoded)

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
