# uritemplate-test

Vendored conformance corpus for the `uri-template` format (RFC 6570).

| | |
| --- | --- |
| Source | <https://github.com/uri-templates/uritemplate-test> |
| Upstream path | repository root |
| Commit | `4171dac22aa67fc710b3f6df308a50bd08552986` |
| Retrieved | 2026-08-01 |
| License | Apache-2.0 (`LICENSE`, retained verbatim) |

## Files

| File | Cases | Expectation |
| --- | --- | --- |
| `spec-examples.json` | 64 | valid |
| `spec-examples-by-section.json` | 117 | valid |
| `extended-tests.json` | 53 | valid |
| `negative-tests.json` | 36 | invalid |

Each file maps a group name to `{"variables": {...}, "testcases": [[template,
expansion], ...]}`. The corpus tests template *expansion*; `corpus_uritemplate_test.go`
consumes element 0 of each test case only, since the format assertion checks the
template *grammar* and has no variable bindings to expand against. Element 1 is a
string in most cases, an array of acceptable expansions in some, and the JSON
boolean `false` throughout `negative-tests.json`.

Five negative cases are grammatically valid and are carved out in the driver;
each carve-out names the RFC 6570 section that makes it an expansion error
rather than a syntax error.
