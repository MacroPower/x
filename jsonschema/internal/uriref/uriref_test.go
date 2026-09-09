package uriref_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// key mints the document key a spelling names from the base-less root, the
// way a $ref in the root document reaches it.
func key(t *testing.T, spelling string) uriref.DocKey {
	t.Helper()

	k, _, err := uriref.Resolve(uriref.DocKey{}, spelling)
	require.NoError(t, err)

	return k
}

// TestResolve pins resolution and canonicalization together: the document a
// reference names, spelled the one way every table keys it, and the fragment
// it carries. The rows cover the opaque-URN merge and its symmetry with the
// absolute spelling, the bare-path base a base-less root carries, and every
// spelling pair that once produced two keys for one document.
func TestResolve(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base    string
		ref     string
		want    string
		name    string // the fragment as an anchor name, when any
		pointer string // the fragment as a pointer, when any
		encoded bool
	}{
		// The opaque-URN symmetry invariant: a relative ref against an opaque
		// base must keep the namespace identifier, so the result matches the
		// absolute URN a caller would register directly.
		"opaque urn relative ref keeps namespace": {base: "urn:example:root", ref: "sub", want: "urn:example:sub"},
		"opaque urn fragment ref": {
			base: "urn:example:root", ref: "#/$defs/foo", want: "urn:example:root",
			name: "/$defs/foo", pointer: "/$defs/foo",
		},
		// RFC 3986 section 5.2.2: a reference path beginning with "/" replaces
		// the base path outright instead of merging with it.
		"opaque urn rooted ref replaces path": {base: "urn:example:a/b", ref: "/c", want: "urn:/c"},
		"opaque urn rooted ref keeps fragment": {
			base: "urn:example:root", ref: "/c#frag", want: "urn:/c", name: "frag",
		},
		"opaque urn rooted dot-segmented ref": {base: "urn:example:a/b", ref: "/x/../c", want: "urn:/c"},
		"opaque urn rooted encoded ref keeps encoding": {
			base: "urn:example:root",
			ref:  "/sub%2Fx",
			want: "urn:/sub%2Fx",
		},
		"empty base keeps the ref": {base: "", ref: "sub", want: "sub"},
		// RFC 3986 5.2.2 applies remove_dot_segments after the merge, so a
		// dot-segmented ref and its canonical absolute spelling compute the
		// same registry key, matching the hierarchical branch.
		"opaque urn parent ref pops a segment": {
			base: "urn:example:a/b/c",
			ref:  "../d",
			want: "urn:example:a/d",
		},
		"opaque urn same-dir dot ref": {
			base: "urn:example:a/b/c",
			ref:  "./d",
			want: "urn:example:a/b/d",
		},
		"opaque urn parent ref stops at the namespace": {
			base: "urn:example:a/b/c",
			ref:  "../../../d",
			want: "urn:example:d",
		},
		"opaque urn slashless parent ref keeps namespace": {
			base: "urn:example:root",
			ref:  "../sub",
			want: "urn:example:sub",
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

		// The opaque merge operates on the encoded form of a relative ref, the
		// same way the hierarchical branch resolves on escaped paths. A
		// percent-escape of a reserved character survives, since decoding it
		// would name a different key than the absolute spelling recomputes.
		"escaped slash stays encoded": {base: "urn:example:root", ref: "a%2Fb", want: "urn:example:a%2Fb"},
		"escaped space stays encoded": {base: "urn:example:root", ref: "su%20b", want: "urn:example:su%20b"},
		"encoded fragment survives the merge": {
			base: "urn:example:root", ref: "sub#/a%2Fb", want: "urn:example:sub",
			name: "/a/b", pointer: "/a%2Fb", encoded: true,
		},

		// A fragment whose leading pointer separator is itself percent-escaped
		// is still a pointer: net/url decodes it to a "/"-led form, and the
		// splitter reads %2F as a separator, so the still-encoded raw text is
		// what a pointer walk must receive.
		"encoded root separator is a pointer": {
			base: "", ref: "#%2F$defs%2Ffoo", want: "",
			name: "/$defs/foo", pointer: "%2F$defs%2Ffoo", encoded: true,
		},

		// A bare-path base, the form a root document with no configured base
		// and every document fetched through it carry. A relative ref used to
		// pass through verbatim under an empty base and take net/url's rooted,
		// dot-segment-free form under a schemeless base, so one file
		// registered under two keys and was fetched twice.
		"empty base keeps a plain path":          {base: "", ref: "dir/a.json", want: "dir/a.json"},
		"empty base drops a dot segment":         {base: "", ref: "./c.json", want: "c.json"},
		"empty base keeps an absolute ref":       {base: "", ref: "http://x/y", want: "http://x/y"},
		"empty base keeps a rooted ref":          {base: "", ref: "/abs.json", want: "/abs.json"},
		"empty base keeps a fragment ref":        {base: "", ref: "#/a", want: "", name: "/a", pointer: "/a"},
		"sibling merges into the base directory": {base: "dir/b.json", ref: "a.json", want: "dir/a.json"},
		"parent segment pops the directory":      {base: "dir/b.json", ref: "../x.json", want: "x.json"},
		"nested directory joins":                 {base: "dir/b.json", ref: "sub/c.json", want: "dir/sub/c.json"},
		"query and fragment carry over": {
			base: "dir/b.json", ref: "a.json?q=1#/x", want: "dir/a.json?q=1", name: "/x", pointer: "/x",
		},
		"fragment against a bare path":      {base: "dir/b.json", ref: "#frag", want: "dir/b.json", name: "frag"},
		"rooted ref replaces a bare path":   {base: "dir/b.json", ref: "/abs.json", want: "/abs.json"},
		"absolute ref replaces a bare path": {base: "dir/b.json", ref: "http://x/y", want: "http://x/y"},
		"bare file name base":               {base: "root.json", ref: "a.json", want: "a.json"},

		// RFC 3986 section 6.2.2: every spelling pair below once named one
		// document under two keys.
		"dot segments in an absolute base": {
			base: "http://x/a/./b/../c/d.json",
			ref:  "e.json",
			want: "http://x/a/c/e.json",
		},
		"scheme and host lowercase":          {base: "", ref: "HTTP://EXAMPLE.com/A", want: "http://example.com/A"},
		"percent-encoding hex uppercase":     {base: "", ref: "http://x/a%2fb", want: "http://x/a%2Fb"},
		"unreserved escape decodes":          {base: "", ref: "http://x/%7Efoo/%41", want: "http://x/~foo/A"},
		"unreserved escape decodes in a urn": {base: "", ref: "urn:example:%7Ea", want: "urn:example:~a"},
		"rooted and bare stay distinct":      {base: "", ref: "/a.json", want: "/a.json"},
		"empty path and slash stay distinct": {base: "", ref: "http://x", want: "http://x"},
		"slash path":                         {base: "", ref: "http://x/", want: "http://x/"},
		"query escapes canonicalize":         {base: "", ref: "http://x/a?q=%7e", want: "http://x/a?q=~"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, frag, err := uriref.Resolve(key(t, tc.base), tc.ref)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())

			if tc.name == "" && tc.pointer == "" {
				assert.True(t, frag.IsEmpty(), "no fragment")

				return
			}

			assert.Equal(t, tc.name, frag.Name())

			raw, encoded := frag.Pointer()
			if tc.pointer != "" {
				assert.True(t, frag.IsPointer())
				assert.Equal(t, tc.pointer, raw)
				assert.Equal(t, tc.encoded, encoded)
			}

			// A key survives a url.Parse round trip byte for byte, the
			// symmetry the registry relies on: the absolute spelling of the
			// same document parses and re-serializes to this exact key.
			parsed, err := url.Parse(got.String())
			require.NoError(t, err)
			assert.Equal(t, got.String(), parsed.String())

			// A key resolved from the root reproduces itself, so the two
			// producers agree.
			again, _, err := uriref.Resolve(uriref.DocKey{}, got.String())
			require.NoError(t, err)
			assert.Equal(t, got, again)
		})
	}
}

// TestResolveSymmetry asserts the registration/lookup symmetry the opaque
// merge exists to preserve: resolving a relative ref against an opaque base
// yields the same key as resolving the absolute URN from the root, so a
// relative $id and the absolute $ref agree on one registry key, for a merged
// ref and for a rooted one.
func TestResolveSymmetry(t *testing.T) {
	t.Parallel()

	registered, _, err := uriref.Resolve(uriref.DocKey{}, "urn:example:sub")
	require.NoError(t, err)

	resolved, _, err := uriref.Resolve(key(t, "urn:example:root"), "sub")
	require.NoError(t, err)
	assert.Equal(t, registered, resolved)
	assert.Equal(t, "urn:example:sub", resolved.String())

	registered, _, err = uriref.Resolve(uriref.DocKey{}, "urn:/c")
	require.NoError(t, err)

	resolved, _, err = uriref.Resolve(key(t, "urn:example:a/b"), "/c")
	require.NoError(t, err)
	assert.Equal(t, registered, resolved)
}

// TestResolveRefusesAnUnparsableRef pins that a reference net/url cannot
// parse is an error rather than a key.
func TestResolveRefusesAnUnparsableRef(t *testing.T) {
	t.Parallel()

	_, _, err := uriref.Resolve(uriref.DocKey{}, "http://[::1")
	require.Error(t, err)
}

// TestParseBase pins the key a configured base mints: a schemeless base is a
// file path under file:///, a Windows drive path is a file URI under its
// drive, an absolute base canonicalizes, a fragment is dropped, and a base
// that is not a URI reference is refused.
func TestParseBase(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		base string
		want string
		err  bool
	}{
		"empty is the zero key":       {base: "", want: ""},
		"absolute passes through":     {base: "https://ex.test/a.json", want: "https://ex.test/a.json"},
		"schemeless resolves to file": {base: "main.json", want: "file:///main.json"},
		"nested schemeless":           {base: "dir/main.json", want: "file:///dir/main.json"},
		"fragment is dropped":         {base: "https://ex.test/a.json#frag", want: "https://ex.test/a.json"},
		"host lowercases":             {base: "HTTPS://EX.TEST/a.json", want: "https://ex.test/a.json"},
		"dot segments collapse":       {base: "https://ex.test/x/../a.json", want: "https://ex.test/a.json"},
		"opaque passes through":       {base: "urn:x:y", want: "urn:x:y"},
		"unparsable is refused":       {base: "http://[::1", err: true},

		// A Windows drive path is the file URI of RFC 8089 appendix E.2. The
		// drive letter keeps its case, a two-letter scheme stays a scheme, and
		// the fragment rule applies as to any other base.
		"drive path":                  {base: "C:/schemas/main.json", want: "file:///C:/schemas/main.json"},
		"drive path with backslashes": {base: `C:\schemas\main.json`, want: "file:///C:/schemas/main.json"},
		"drive path fragment dropped": {base: "C:/schemas/main.json#x", want: "file:///C:/schemas/main.json"},
		"drive root":                  {base: `C:\`, want: "file:///C:/"},
		"lowercase drive keeps case":  {base: "c:/schemas/main.json", want: "file:///c:/schemas/main.json"},
		"two-letter scheme stays":     {base: "ab:x/y.json", want: "ab:x/y.json"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := uriref.ParseBase(tc.base)
			if tc.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())
			assert.Equal(t, tc.want == "", got.IsZero())
		})
	}
}

// TestParseBaseDrivePathResolves pins that a reference joins under the drive
// of a drive-path base: a sibling name, a parent-relative path, and a
// fragment all resolve as against any other file URI.
func TestParseBaseDrivePathResolves(t *testing.T) {
	t.Parallel()

	base, err := uriref.ParseBase(`C:\schemas\main.json`)
	require.NoError(t, err)

	tests := map[string]struct {
		ref     string
		want    string
		pointer string
	}{
		"sibling":         {ref: "sub.json", want: "file:///C:/schemas/sub.json"},
		"parent relative": {ref: "../other/x.json", want: "file:///C:/other/x.json"},
		"fragment only":   {ref: "#/a", want: "file:///C:/schemas/main.json", pointer: "/a"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, frag, err := uriref.Resolve(base, tc.ref)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())
			assert.Equal(t, tc.pointer, frag.Name())
		})
	}
}

// TestLocationAndAnchor pins the one producer of "#": a location spells the
// document, "#", and the pointer, and an anchor key the document, "#", and
// the name, with the zero key spelling a bare "#".
func TestLocationAndAnchor(t *testing.T) {
	t.Parallel()

	doc := key(t, "https://example.com/s")

	assert.Equal(t, "#", uriref.DocKey{}.At("").String())
	assert.Equal(t, "#/a", uriref.DocKey{}.At("/a").String())
	assert.Equal(t, "https://example.com/s#/a/b", doc.At("/a/b").String())
	assert.Equal(t, "https://example.com/s#", doc.At("").String())
	assert.Equal(t, "https://example.com/s#a", doc.Anchor("a").String())
	assert.Equal(t, "#a", uriref.DocKey{}.Anchor("a").String())
	assert.Equal(t, "https://example.com/s#a.b", doc.Anchor("a.b").String())
	assert.Equal(t, "urn:example:s#a", key(t, "urn:example:s").Anchor("a").String())
}

func TestIsFragmentOnly(t *testing.T) {
	t.Parallel()

	assert.True(t, uriref.IsFragmentOnly("#foo"))
	assert.True(t, uriref.IsFragmentOnly("#"))
	assert.False(t, uriref.IsFragmentOnly("foo#bar"))
	assert.False(t, uriref.IsFragmentOnly(""))
}
