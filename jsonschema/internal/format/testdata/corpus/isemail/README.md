# is_email

Vendored conformance corpus for the `email` and `idn-email` formats.

| | |
| --- | --- |
| Source | <https://github.com/dominicsayers/isemail> |
| Upstream path | `test/tests.xml` |
| Commit | `cfeefc3f2f88cb195053f6a309fa4f640cd369b5` |
| Retrieved | 2026-08-01 |
| License | BSD-3-Clause (`LICENSE`, upstream `license.md`, retained verbatim) |

## Shape

164 `<test>` elements, each carrying `id` as an attribute and `address`,
`category`, `diagnosis`, `source`, and `sourcelink` as child elements. Case
`id="1"` has a self-closing `<address/>`, which is the empty-string test.

Control characters are stored as U+2400 + n (the "SYMBOL FOR" block) because XML
cannot carry them; `corpus_email_test.go` maps them back, special-casing U+2421
SYMBOL FOR DELETE to U+007F.

The corpus grades an address on the RFC 5322 spectrum, while these formats assert
the RFC 5321 `Mailbox` grammar, so the driver maps each `category` to an
accept/reject expectation rather than reading `diagnosis` directly.
