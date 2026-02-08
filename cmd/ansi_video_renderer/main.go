// Command ansi_video_renderer renders video frames in the terminal using
// ANSI-colored half-block characters.
//
// Each terminal cell represents two vertical pixels via foreground and
// background colors on the "▀" (upper half block) character.
// The command accepts either a video file (frames are extracted via ffmpeg) or
// a directory of PNG frames.
//
// # Usage
//
//	ansi_video_renderer [flags] <video_file|frame_directory>
//
// # Flags
//
//	-fps int    playback FPS (default 24)
//	-w int      render width in columns (0 = auto-detect terminal width)
//	-loop       loop playback continuously
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	tea "charm.land/bubbletea/v2"
)

func main() {
	os.Exit(run0())
}

func run0() int {
	fps := flag.Int("fps", 24, "playback FPS")
	width := flag.Int("w", 0, "render width in columns (0 = auto-detect)")
	loop := flag.Bool("loop", false, "loop playback continuously")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ansi_video_renderer [flags] <video_file|frame_directory>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()

		return 1
	}

	input := flag.Arg(0)

	info, err := os.Stat(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)

		return 1
	}

	frameDir := input

	if !info.IsDir() {
		dir, cleanup, extractErr := extractFrames(input, *fps)
		if extractErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", extractErr)

			return 1
		}

		defer cleanup()

		frameDir = dir
	}

	frames, err := loadFrames(frameDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)

		return 1
	}

	cols := *width

	var rows int

	if cols == 0 {
		w, h, termErr := term.GetSize(int(os.Stdout.Fd()))
		if termErr != nil {
			fmt.Fprintf(os.Stderr, "Error: unable to detect terminal size (use -w flag): %v\n", termErr)

			return 1
		}

		cols = w
		rows = h
	} else {
		// Estimate rows from cols using a 16:9 aspect ratio.
		rows = cols * 9 / 16 / 2
	}

	// Pre-resize all frames.
	resized := make([]*image.RGBA, len(frames))
	for i, f := range frames {
		resized[i] = resizeImage(f, cols, rows)
	}

	p := tea.NewProgram(newModel(resized, frames, *fps, cols, rows, *loop))

	_, err = p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)

		return 1
	}

	return 0
}

// tickMsg signals that it is time to advance to the next frame.
type tickMsg struct{}

// model is the bubbletea model for the ANSI video renderer.
type model struct {
	origFrames []image.Image // Original (unresized) source frames.
	frames     []*image.RGBA // Pre-resized frames for the current terminal size.
	buf        strings.Builder
	fps        int
	cols       int
	rows       int
	index      int
	loop       bool
	done       bool
}

func newModel(resized []*image.RGBA, orig []image.Image, fps, cols, rows int, loop bool) *model {
	return &model{
		origFrames: orig,
		frames:     resized,
		fps:        fps,
		cols:       cols,
		rows:       rows,
		loop:       loop,
	}
}

// Init returns the first tick command to start playback.
func (m *model) Init() tea.Cmd {
	return tea.Tick(time.Second/time.Duration(m.fps), func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

// Update handles tick, resize, and quit messages.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.cols = msg.Width
		m.rows = msg.Height

		for i, f := range m.origFrames {
			m.frames[i] = resizeImage(f, m.cols, m.rows)
		}

	case tickMsg:
		if len(m.frames) <= 1 || m.done {
			return m, nil
		}

		m.index++

		if m.index >= len(m.frames) {
			if m.loop {
				m.index = 0
			} else {
				m.index = len(m.frames) - 1
				m.done = true

				return m, nil
			}
		}

		return m, tea.Tick(time.Second/time.Duration(m.fps), func(time.Time) tea.Msg {
			return tickMsg{}
		})
	}

	return m, nil
}

// View renders the current frame as ANSI half-block characters.
func (m *model) View() tea.View {
	m.buf.Reset()
	renderFrame(m.frames[m.index], m.cols, m.rows, &m.buf)

	v := tea.NewView(m.buf.String())
	v.AltScreen = true

	return v
}
