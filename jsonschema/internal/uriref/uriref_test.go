package uriref_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

func TestResolveURI(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base string
		ref  string
		want string
	}{
		// The opaque-URN symmetry invariant: a relative ref against an opaque
		// base must keep the namespace identifier, so the result matches the
		// absolute URN a caller would register directly.
		"opaque urn relative ref keeps namespace": {
			base: "urn:example:root",
			ref:  "sub",
			want: "urn:example:sub",
		},
		"opaque urn fragment ref": {
			base: "urn:example:root",
			ref:  "#/$defs/foo",
			want: "urn:example:root#/$defs/foo",
		},
		"empty base returns ref": {
			base: "",
			ref:  "sub",
			want: "sub",
		},
		"hierarchical relative ref merges path": {
			base: "http://example.com/a/b",
			ref:  "c",
			want: "http://example.com/a/c",
		},
		"absolute ref replaces base": {
			base: "http://example.com/a/b",
			ref:  "http://other.com/x",
			want: "http://other.com/x",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, uriref.ResolveURI(tc.base, tc.ref))
		})
	}
}

// TestResolveURIOpaqueSymmetry asserts the registration/lookup symmetry the
// opaque merge exists to preserve: resolving a relative ref against an opaque
// base yields the same key as directly resolving the absolute URN against an
// empty base, so a relative $id and the absolute $ref agree on one registry
// key.
func TestResolveURIOpaqueSymmetry(t *testing.T) {
	t.Parallel()

	const base = "urn:example:root"

	registered := uriref.ResolveURI("", "urn:example:sub")
	resolved := uriref.ResolveURI(base, "sub")

	assert.Equal(t, registered, resolved)
	assert.Equal(t, "urn:example:sub", resolved)
	assert.NotEqual(t, "urn:sub", resolved)
}

func TestRawFragment(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		uri         string
		want        string
		wantEncoded bool
	}{
		// A percent-escaped separator leaves the fragment in its still-encoded
		// RawFragment form so the caller splits before decoding.
		"percent-escaped separator stays encoded": {
			uri:         "http://example.com/#/a%2Fb",
			want:        "/a%2Fb",
			wantEncoded: true,
		},
		"plain pointer is already decoded": {
			uri:         "http://example.com/#/foo",
			want:        "/foo",
			wantEncoded: false,
		},
		"plain anchor is already decoded": {
			uri:         "http://example.com/#plain",
			want:        "plain",
			wantEncoded: false,
		},
		"no fragment": {
			uri:         "http://example.com/x",
			want:        "",
			wantEncoded: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			u, err := url.Parse(tc.uri)
			require.NoError(t, err)

			raw, encoded := uriref.RawFragment(u)
			assert.Equal(t, tc.want, raw)
			assert.Equal(t, tc.wantEncoded, encoded)
		})
	}
}

func TestStripFragment(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		uri  string
		want string
	}{
		"removes pointer fragment": {
			uri:  "http://example.com/a#/foo",
			want: "http://example.com/a",
		},
		"removes encoded fragment": {
			uri:  "http://example.com/a#/a%2Fb",
			want: "http://example.com/a",
		},
		"no fragment passes through": {
			uri:  "http://example.com/a",
			want: "http://example.com/a",
		},
		"opaque urn fragment removed": {
			uri:  "urn:example:root#/foo",
			want: "urn:example:root",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, uriref.StripFragment(tc.uri))
		})
	}
}

func TestIsFragmentOnly(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		uri  string
		want bool
	}{
		"bare hash is fragment only": {
			uri:  "#",
			want: true,
		},
		"hash pointer is fragment only": {
			uri:  "#/$defs/foo",
			want: true,
		},
		"hash anchor is fragment only": {
			uri:  "#anchor",
			want: true,
		},
		"absolute uri is not fragment only": {
			uri:  "http://example.com/a#/foo",
			want: false,
		},
		"relative ref is not fragment only": {
			uri:  "sub",
			want: false,
		},
		"empty is not fragment only": {
			uri:  "",
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, uriref.IsFragmentOnly(tc.uri))
		})
	}
}

func TestNormalizeBaseURI(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base string
		want string
	}{
		"empty passes through": {
			base: "",
			want: "",
		},
		"schemeless path resolves against file root": {
			base: "main.json",
			want: "file:///main.json",
		},
		"absolute uri passes through": {
			base: "http://example.com/a",
			want: "http://example.com/a",
		},
		"file uri passes through": {
			base: "file:///main.json",
			want: "file:///main.json",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, uriref.NormalizeBaseURI(tc.base))
		})
	}
}

func TestAnchorKey(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base string
		name string
		want string
	}{
		"absolute base": {base: "https://example.com/s", name: "a", want: "https://example.com/s#a"},
		"empty base":    {base: "", name: "a", want: "#a"},
		"urn base":      {base: "urn:example:s", name: "a", want: "urn:example:s#a"},
		"empty name":    {base: "https://example.com/s", name: "", want: "https://example.com/s#"},
		"dotted name":   {base: "https://example.com/s", name: "a.b", want: "https://example.com/s#a.b"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, uriref.AnchorKey(tc.base, tc.name))
		})
	}
}

func TestIDBase(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base string
		id   string
		want string
	}{
		"relative id resolves against base": {
			base: "https://example.com/root.json",
			id:   "child.json",
			want: "https://example.com/child.json",
		},
		"absolute id replaces base": {
			base: "https://example.com/root.json",
			id:   "https://other.test/x.json",
			want: "https://other.test/x.json",
		},
		"fragment is stripped": {
			base: "https://example.com/root.json",
			id:   "child.json#section",
			want: "https://example.com/child.json",
		},
		"empty base keeps id": {
			base: "",
			id:   "https://example.com/x.json",
			want: "https://example.com/x.json",
		},
		"opaque urn base merges path": {
			base: "urn:example:root",
			id:   "child",
			want: "urn:example:child",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := uriref.IDBase(tc.base, tc.id)
			assert.Equal(t, tc.want, got)

			// IDBase agrees with the key a $ref recomputes: exactly the resolved
			// id with its fragment removed.
			assert.Equal(t, uriref.StripFragment(uriref.ResolveURI(tc.base, tc.id)), got)
		})
	}
}
