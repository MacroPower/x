# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

This file covers the `ansivideo` module only. It is part of the repo's go.work
workspace.

## Architecture

The package is three small, independent layers that share the half-block model
(each terminal cell is two vertically stacked pixels: foreground = top pixel,
background = bottom pixel of "▀"):

- **Rendering** (`render.go`): pure functions. `Resize` scales an
  `image.Image` into a `cols x rows*2` RGBA canvas (aspect-preserving,
  centered, black-padded); `RenderFrame` turns an `*image.RGBA` into the
  truecolor escape sequences that draw it. No I/O, no dependencies on the
  other layers — this is the most reusable and most heavily tested code.
- **Sources** (`source.go`, `ffmpeg.go`): `Source` is the frame-producer
  interface (`ReadFrame`/`Close`); `SourceFunc` is the factory the player
  calls to (re)open a source at a given pixel size. `FFmpegSource` is the only
  built-in implementation — it pipes ffmpeg's rawvideo output, scaled and
  padded to an exact canvas. `FFmpeg` adapts it to a `SourceFunc`.
- **Playback** (`player.go`): `Player` is the `bubbletea/v2.Model`. It owns the
  state machine — buffer warmup before ticking, fixed-rate frame advance,
  resize-triggered source restart, looping, and clean shutdown.

The CLI (`cmd/ansivideo`) is a thin wrapper: flag parsing, terminal-size
detection, and wiring `FFmpeg` into a `Player`.

### Player state machine

`Player` is driven entirely by messages (`tea.Msg`); `Update` dispatches to one
`handle*` method per message type. Key invariants:

- The first frame is shown immediately; ticking only starts after `fps/2`
  frames buffer, so ffmpeg startup latency does not eat tick intervals.
- A read command (`readFrame`) binds the current source into its closure, so a
  frame in flight when the source is swapped (resize/loop) cannot be consumed
  from the replacement.
- Ticks carry the playback epoch (`gen`, bumped on every open). `tea.Tick` has
  no cancellation, so a tick scheduled before a restart still fires;
  `handleTick` discards any tick whose `gen` no longer matches, so a restart
  cannot leave a second tick chain running (which would speed playback up).
- Resize re-opens the source only when the pixel dimensions actually change
  (`srcW`/`srcH` guard), avoiding redundant ffmpeg processes on resize bursts.
- An unexpected source error (not an `io.EOF` family error) is stored on `err`
  and quits; the CLI surfaces it via `Player.Err` after the program exits, so
  the library never writes to stderr itself.

Frames are read ahead into memory without backpressure (one outstanding read at
a time, drained at the tick rate but never paused), so the player is meant for
short clips, not arbitrarily long streams. `doc.go` states this contract.

## Conventions

- Behavior is documented in `doc.go`; keep it in sync with the code.
- `RenderFrame` takes a concrete `*image.RGBA` for the pixel-pushing hot path,
  not `image.Image`; `Resize` returns exactly that type, so the two compose.

## Tests

All tests are external (`package ansivideo_test`), per the repo's `testpackage`
policy. `export_test.go` (in-package, matched by `testpackage`'s skip regexp)
exposes the unexported messages and a few state accessors so the external
package can drive the `Player` deterministically, without real timers or
sources.

- `ffmpeg_test.go` has one real-ffmpeg test named with `Integration`. The
  unit-only target (`task go:test`) runs `-skip Integration` and excludes it,
  while the aggregate `task test` also runs it through `go:test:integration`.
  It generates a clip with ffmpeg and decodes it, skipping if ffmpeg is absent.
- `TestNewFFmpegSourceReportsMissingFFmpeg` uses `t.Setenv`, which is
  incompatible with `t.Parallel`, so it carries a `//nolint:paralleltest`.
