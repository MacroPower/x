// Command ansivideo plays a video in the terminal using ANSI-colored
// half-block characters: each cell encodes two vertical pixels via the
// foreground and background colors of the "▀" glyph. It shells out to ffmpeg
// to decode frames, so ffmpeg must be installed and on PATH.
//
// # Usage
//
//	ansivideo [flags] <video_file>
//
// # Flags
//
//	-fps int   playback frame rate (default 24)
//	-w int     render width in columns (0 = auto-detect terminal width)
//	-loop      loop playback continuously
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"golang.org/x/term"

	tea "charm.land/bubbletea/v2"

	"go.jacobcolvin.com/x/ansivideo"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses flags from args, plays the requested video, and returns a process
// exit code, writing usage and diagnostics to stderr. It returns 2 for a usage
// error, 1 for a playback failure, and 0 on success.
func run(args []string) int {
	fs := flag.NewFlagSet("ansivideo", flag.ContinueOnError)

	fps := fs.Int("fps", 24, "playback frame rate")
	width := fs.Int("w", 0, "render width in columns (0 = auto-detect terminal width)")
	loop := fs.Bool("loop", false, "loop playback continuously")

	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: ansivideo [flags] <video_file>\n\nFlags:\n")
		fs.PrintDefaults()
	}

	err := fs.Parse(args)
	if err != nil {
		return 2
	}

	if fs.NArg() != 1 {
		fs.Usage()

		return 2
	}

	err = play(fs.Arg(0), *fps, *width, *loop)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ansivideo: %v\n", err)

		return 1
	}

	return 0
}

// play decodes input with ffmpeg and plays it to the terminal, returning the
// first error from sizing, the bubbletea program, or the player.
func play(input string, fps, width int, loop bool) error {
	cols, rows, err := terminalSize(width)
	if err != nil {
		return err
	}

	player := ansivideo.NewPlayer(
		ansivideo.FFmpeg(context.Background(), input, fps),
		ansivideo.WithFPS(fps),
		ansivideo.WithSize(cols, rows),
		ansivideo.WithLoop(loop),
	)

	_, err = tea.NewProgram(player).Run()
	if err != nil {
		return err
	}

	return player.Err()
}

// terminalSize returns the render grid in cells. A positive width is used
// directly, with the row count derived from a 16:9 frame; otherwise the size is
// detected from the terminal on stdout.
func terminalSize(width int) (int, int, error) {
	if width > 0 {
		// Two pixels per cell over a 16:9 frame: rows = width * 9/16 / 2.
		return width, width * 9 / 16 / 2, nil
	}

	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 0, 0, fmt.Errorf("detecting terminal size (use -w to set width): %w", err)
	}

	return cols, rows, nil
}
