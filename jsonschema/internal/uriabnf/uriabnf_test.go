package uriabnf_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/uriabnf"
)

// TestURI pins the RFC 3986 section 1.1.2 examples as URIs, the section 5.4
// reference inputs as references that are not URIs, and the corrections the
// net/url-based validator had to make by hand: a malformed pct-encoded triplet
// in the query, a second "@" in the authority, and a percent-encoded ASCII
// octet in a reg-name.
func TestURI(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input        string
		uri          bool
		uriReference bool
	}{
		// RFC 3986 section 1.1.2.
		"ftp":    {input: "ftp://ftp.is.co.za/rfc/rfc1808.txt", uri: true, uriReference: true},
		"http":   {input: "http://www.ietf.org/rfc/rfc2396.txt", uri: true, uriReference: true},
		"ldap":   {input: "ldap://[2001:db8::7]/c=GB?objectClass?one", uri: true, uriReference: true},
		"mailto": {input: "mailto:John.Doe@example.com", uri: true, uriReference: true},
		"news":   {input: "news:comp.infosystems.www.servers.unix", uri: true, uriReference: true},
		"tel":    {input: "tel:+1-816-555-1212", uri: true, uriReference: true},
		"telnet": {input: "telnet://192.0.2.16:80/", uri: true, uriReference: true},
		"urn":    {input: "urn:oasis:names:specification:docbook:dtd:xml:4.1.2", uri: true, uriReference: true},

		// RFC 3986 section 5.4 reference inputs, relative unless they carry a
		// scheme.
		"g:h":                          {input: "g:h", uri: true, uriReference: true},
		"g":                            {input: "g", uriReference: true},
		"./g":                          {input: "./g", uriReference: true},
		"g/":                           {input: "g/", uriReference: true},
		"/g":                           {input: "/g", uriReference: true},
		"//g":                          {input: "//g", uriReference: true},
		"?y":                           {input: "?y", uriReference: true},
		"g?y":                          {input: "g?y", uriReference: true},
		"#s":                           {input: "#s", uriReference: true},
		"g#s":                          {input: "g#s", uriReference: true},
		"g?y#s":                        {input: "g?y#s", uriReference: true},
		";x":                           {input: ";x", uriReference: true},
		"g;x":                          {input: "g;x", uriReference: true},
		"g;x?y#s":                      {input: "g;x?y#s", uriReference: true},
		"empty":                        {input: "", uriReference: true},
		".":                            {input: ".", uriReference: true},
		"./":                           {input: "./", uriReference: true},
		"..":                           {input: "..", uriReference: true},
		"../":                          {input: "../", uriReference: true},
		"../g":                         {input: "../g", uriReference: true},
		"../..":                        {input: "../..", uriReference: true},
		"../../":                       {input: "../../", uriReference: true},
		"../../g":                      {input: "../../g", uriReference: true},
		"../../../g":                   {input: "../../../g", uriReference: true},
		"/./g":                         {input: "/./g", uriReference: true},
		"/../g":                        {input: "/../g", uriReference: true},
		"g.":                           {input: "g.", uriReference: true},
		".g":                           {input: ".g", uriReference: true},
		"g..":                          {input: "g..", uriReference: true},
		"..g":                          {input: "..g", uriReference: true},
		"./../g":                       {input: "./../g", uriReference: true},
		"./g/.":                        {input: "./g/.", uriReference: true},
		"g/./h":                        {input: "g/./h", uriReference: true},
		"g/../h":                       {input: "g/../h", uriReference: true},
		"g;x=1/./y":                    {input: "g;x=1/./y", uriReference: true},
		"g;x=1/../y":                   {input: "g;x=1/../y", uriReference: true},
		"g?y/./x":                      {input: "g?y/./x", uriReference: true},
		"g?y/../x":                     {input: "g?y/../x", uriReference: true},
		"g#s/./x":                      {input: "g#s/./x", uriReference: true},
		"g#s/../x":                     {input: "g#s/../x", uriReference: true},
		"http:g":                       {input: "http:g", uri: true, uriReference: true},
		"colon in segment after first": {input: "./a:b", uriReference: true},
		"colon in first segment":       {input: "1abc:x"},

		// The net/url corrections.
		"malformed pct in query":  {input: "http://a/b?%zz"},
		"second @ in authority":   {input: "http://a@b@c/"},
		"pct-encoded ascii host":  {input: "http://ex%41mple.com/", uri: true, uriReference: true},
		"malformed pct in path":   {input: "http://a/b%zz"},
		"pct at end":              {input: "http://a/b%4"},
		"userinfo with colon":     {input: "http://user:pa:ss@host/", uri: true, uriReference: true},
		"empty port":              {input: "http://a:/", uri: true, uriReference: true},
		"letters in port":         {input: "http://a:xyz/"},
		"two colons in host":      {input: "http://a:1:2/"},
		"empty authority":         {input: "http:///path", uri: true, uriReference: true},
		"empty hier-part":         {input: "http:", uri: true, uriReference: true},
		"network path only":       {input: "//", uriReference: true},
		"space":                   {input: "http://exa mple.com"},
		"second fragment":         {input: "a#b#c"},
		"bracket in path":         {input: "http://a/[b]"},
		"control":                 {input: "http://a/\x01"},
		"invalid utf8":            {input: "http://a/\xff"},
		"scheme with digit first": {input: "1http://a/"},
		"scheme with plus":        {input: "a+b-c.d:x", uri: true, uriReference: true},
		"query with slash and ?":  {input: "http://a/?q=/x?y", uri: true, uriReference: true},
		"fragment with slash":     {input: "http://a/#/x?y", uri: true, uriReference: true},

		// IP literals.
		"ipv6":                                {input: "http://[::1]/", uri: true, uriReference: true},
		"ipv6 full":                           {input: "http://[1:2:3:4:5:6:7:8]/", uri: true, uriReference: true},
		"ipv6 with ipv4 tail":                 {input: "http://[::ffff:192.0.2.1]/", uri: true, uriReference: true},
		"ipv6 seven then ::":                  {input: "http://[1:2:3:4:5:6:7::]/", uri: true, uriReference: true},
		"ipv6 eight with ::":                  {input: "http://[1:2:3:4:5:6:7::8]/"},
		"ipv6 two ::":                         {input: "http://[1::2::3]/"},
		"ipv6 nine groups":                    {input: "http://[1:2:3:4:5:6:7:8:9]/"},
		"ipv6 seven groups":                   {input: "http://[1:2:3:4:5:6:7]/"},
		"ipv6 long group":                     {input: "http://[12345::]/"},
		"ipv6 bare":                           {input: "http://::1/"},
		"ipv6 unclosed":                       {input: "http://[::1/"},
		"ipv6 zone":                           {input: "http://[fe80::1%25eth0]/"},
		"ipvfuture":                           {input: "http://[v7.x:y]/", uri: true, uriReference: true},
		"ipvfuture upper":                     {input: "http://[V7.x]/", uri: true, uriReference: true},
		"ipvfuture no dot":                    {input: "http://[v7x]/"},
		"ipvfuture no tail":                   {input: "http://[v7.]/"},
		"ipv4":                                {input: "http://192.0.2.16/", uri: true, uriReference: true},
		"ipv4 leading zero":                   {input: "http://192.0.2.016/", uri: true, uriReference: true},
		"ipv4 as ipv6 tail with leading zero": {input: "http://[::192.0.2.016]/"},
		"ipv6 tail 256":                       {input: "http://[::1.2.3.256]/"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.uri, uriabnf.URI(tc.input), "URI(%q)", tc.input)
			assert.Equal(t, tc.uriReference, uriabnf.URIReference(tc.input), "URIReference(%q)", tc.input)
			assert.Equal(t, tc.uri, uriabnf.IRI(tc.input), "IRI(%q) agrees on ASCII input", tc.input)
			assert.Equal(
				t,
				tc.uriReference,
				uriabnf.IRIReference(tc.input),
				"IRIReference(%q) agrees on ASCII input",
				tc.input,
			)
		})
	}
}

// TestIRI pins the character sets the IRI grammar adds: ucschar in every
// component, iprivate in the query alone, and nothing else above ASCII.
func TestIRI(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input        string
		iri          bool
		iriReference bool
	}{
		"ucschar in path":          {input: "https://example.com/\u00e9", iri: true, iriReference: true},
		"ucschar in host":          {input: "https://m\u00fcnchen.de/", iri: true, iriReference: true},
		"ucschar in query":         {input: "https://a/?q=\u00e9", iri: true, iriReference: true},
		"ucschar in fragment":      {input: "https://a/#\u00e9", iri: true, iriReference: true},
		"ucschar in userinfo":      {input: "https://\u00e9@a/", iri: true, iriReference: true},
		"ucschar relative":         {input: "\u00e9/x", iriReference: true},
		"iprivate in query":        {input: "https://a/?\ue000", iri: true, iriReference: true},
		"iprivate in path":         {input: "https://a/\ue000"},
		"iprivate in fragment":     {input: "https://a/#\ue000"},
		"iprivate in host":         {input: "https://\ue000/"},
		"noncharacter":             {input: "https://a/\ufdd0"},
		"plane-final noncharacter": {input: "https://a/\U0001fffe"},
		"tags block":               {input: "https://a/\U000e0001"},
		"c1 control":               {input: "https://a/\u0085"},
		"nbsp":                     {input: "https://a/\u00a0", iri: true, iriReference: true},
		"supplementary plane":      {input: "https://a/\U0001f600", iri: true, iriReference: true},
		"plane 14 ucschar":         {input: "https://a/\U000e1000", iri: true, iriReference: true},
		"ipv6 stays ascii":         {input: "https://[::\u00e9]/"},
		"scheme stays ascii":       {input: "\u00e9:x"},
		"port stays ascii":         {input: "https://a:\u0661/"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.iri, uriabnf.IRI(tc.input), "IRI(%q)", tc.input)
			assert.Equal(t, tc.iriReference, uriabnf.IRIReference(tc.input), "IRIReference(%q)", tc.input)
			assert.False(t, uriabnf.URI(tc.input), "URI(%q) admits nothing above ASCII", tc.input)
			assert.False(t, uriabnf.URIReference(tc.input), "URIReference(%q) admits nothing above ASCII", tc.input)
		})
	}
}
