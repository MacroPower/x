// ecma_regex.mjs answers whether V8 compiles a pattern. It reads one
// hex-encoded UTF-8 pattern per line on stdin and writes one JSON object per
// line on stdout: {"ok":true} when `new RegExp(pattern)` succeeds and
// {"ok":false,"err":...} with the SyntaxError message when it throws. Hex
// keeps every code point, a line terminator included, off the line protocol.
// The pattern is compiled with no flags, because the JSON Schema regex format
// is the bare Annex B pattern and no flag reaches it. oracle_v8_test.go owns
// this process.
import { createInterface } from "node:readline";

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });

lines.on("line", (line) => {
  let answer;

  try {
    new RegExp(Buffer.from(line, "hex").toString("utf8"));
    answer = { ok: true };
  } catch (error) {
    answer = { ok: false, err: String(error.message) };
  }

  process.stdout.write(JSON.stringify(answer) + "\n");
});
