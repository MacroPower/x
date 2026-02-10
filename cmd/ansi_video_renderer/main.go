// Command ansi_video_renderer renders video frames in the terminal using
// ANSI-colored half-block characters.
//
// Each terminal cell represents two vertical pixels via foreground and
// background colors on the "▀" (upper half block) character.
//
// # Usage
//
//	ansi_video_renderer [flags] <video_file>
//
// # Flags
//
//	-fps int    playback FPS (default 24)
//	-w int      render width in columns (0 = auto-detect terminal width)
//	-loop       loop playback continuously
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
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
		fmt.Fprintf(os.Stderr, "Usage: ansi_video_renderer [flags] <video_file>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()

		return 1
	}

	input := flag.Arg(0)

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

	pixW := cols
	pixH := rows * 2

	stream, err := newFrameStream(context.Background(), input, *fps, pixW, pixH)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)

		return 1
	}

	defer stream.stop()

	p := tea.NewProgram(newModel(stream, *fps, cols, rows, *loop, input))

	_, err = p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)

		return 1
	}

	return 0
}

// tickMsg signals that it is time to advance to the next frame.
type tickMsg struct{}

// model is the bubbletea model for streaming frames from an ffmpeg pipe.
type model struct {
	stream    *frameStream
	current   *image.RGBA // Frame currently being displayed.
	resized   *image.RGBA // Current after terminal resize.
	videoPath string
	buf       strings.Builder
	pending   []*image.RGBA
	fps       int
	cols      int
	rows      int
	loop      bool
	done      bool
	ticking   bool
}

func newModel(stream *frameStream, fps, cols, rows int, loop bool, videoPath string) *model {
	return &model{
		stream:    stream,
		fps:       fps,
		cols:      cols,
		rows:      rows,
		loop:      loop,
		videoPath: videoPath,
	}
}

// Init starts the frame reader. The playback tick is deferred until the first
// frame arrives so that FFmpeg startup latency does not waste tick intervals.
func (m *model) Init() tea.Cmd {
	return m.stream.readFrame()
}

// Update handles incoming frames, stream completion, ticks, resize, and quit.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.stream.stop()

			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.cols = msg.Width
		m.rows = msg.Height

		// Only restart if the new dimensions differ from the stream's
		// current resolution. This avoids spawning redundant ffmpeg
		// processes when rapid resize events fire.
		if m.stream.pixW != m.cols || m.stream.pixH != m.rows*2 {
			cmd := m.resetStream()

			return m, cmd
		}

		m.resized = nil // Invalidate; View will re-resize.

	case frameMsg:
		if m.current == nil {
			// First frame: display immediately but don't start ticking yet.
			m.current = msg.frame

			return m, m.stream.readFrame()
		}

		m.pending = append(m.pending, msg.frame)

		if !m.ticking && len(m.pending) >= m.fps/2 {
			// Buffer is warm enough — start playback.
			m.ticking = true

			return m, tea.Batch(
				m.stream.readFrame(),
				tea.Tick(time.Second/time.Duration(m.fps), func(time.Time) tea.Msg {
					return tickMsg{}
				}),
			)
		}

		return m, m.stream.readFrame()

	case streamDoneMsg:
		if msg.Err != nil && !errors.Is(msg.Err, io.EOF) && !errors.Is(msg.Err, io.ErrUnexpectedEOF) {
			fmt.Fprintf(os.Stderr, "stream error: %v\n", msg.Err)
		}

		m.done = true

		// Stream ended during buffering — start ticking with whatever we have.
		if !m.ticking && m.current != nil {
			m.ticking = true

			return m, tea.Tick(time.Second/time.Duration(m.fps), func(time.Time) tea.Msg {
				return tickMsg{}
			})
		}

		return m, nil

	case tickMsg:
		if len(m.pending) > 0 {
			m.current = m.pending[0]
			m.pending = m.pending[1:]
			m.resized = nil // New frame, invalidate resize cache.
		} else if m.done {
			if m.loop {
				cmd := m.restartStream()

				return m, cmd
			}

			if m.current != nil {
				// Hold last frame, but stop ticking.
				return m, nil
			}

			return m, tea.Quit
		}
		// If pending is empty but stream is still running, hold current frame.

		return m, tea.Tick(time.Second/time.Duration(m.fps), func(time.Time) tea.Msg {
			return tickMsg{}
		})
	}

	return m, nil
}

// resetStream stops the current ffmpeg stream and starts a fresh one at the
// current terminal dimensions. It resets all playback state so that buffering
// begins anew. Both loop-restart and terminal-resize use this.
func (m *model) resetStream() tea.Cmd {
	m.stream.stop()

	m.done = false
	m.ticking = false
	m.current = nil
	m.resized = nil
	m.pending = m.pending[:0]

	pixW := m.cols
	pixH := m.rows * 2

	stream, err := newFrameStream(context.Background(), m.videoPath, m.fps, pixW, pixH)
	if err != nil {
		return func() tea.Msg {
			return streamDoneMsg{Err: err}
		}
	}

	m.stream = stream

	return m.stream.readFrame()
}

// restartStream stops the current stream and starts a new one for looping.
// Unlike resetStream, it preserves m.current so View() keeps showing the last
// frame while the new stream buffers, avoiding a flash between loops.
func (m *model) restartStream() tea.Cmd {
	m.stream.stop()

	m.done = false
	m.ticking = false
	m.resized = nil
	m.pending = m.pending[:0]

	pixW := m.cols
	pixH := m.rows * 2

	stream, err := newFrameStream(context.Background(), m.videoPath, m.fps, pixW, pixH)
	if err != nil {
		return func() tea.Msg {
			return streamDoneMsg{Err: err}
		}
	}

	m.stream = stream

	return m.stream.readFrame()
}

// View renders the current frame, resizing on-the-fly for the current terminal
// dimensions.
func (m *model) View() tea.View {
	if m.current == nil {
		v := tea.NewView("Loading...")
		v.AltScreen = true

		return v
	}

	if m.resized == nil {
		if b := m.current.Bounds(); b.Dx() == m.cols && b.Dy() == m.rows*2 {
			m.resized = m.current
		} else {
			m.resized = resizeImage(m.current, m.cols, m.rows)
		}
	}

	m.buf.Reset()
	renderFrame(m.resized, m.cols, m.rows, &m.buf)

	v := tea.NewView(m.buf.String())
	v.AltScreen = true

	return v
}
