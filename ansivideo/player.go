package ansivideo

import (
	"errors"
	"image"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Default playback settings used until [WithFPS] or [WithSize] override them
// and, for the size, until the first [tea.WindowSizeMsg] arrives.
const (
	defaultFPS  = 24
	defaultCols = 80
	defaultRows = 24
)

// frameMsg carries a decoded frame from the source.
type frameMsg struct {
	frame *image.RGBA
}

// tickMsg signals that it is time to advance to the next frame. Gen identifies
// the playback epoch (source) the tick was scheduled for; a tick whose gen no
// longer matches the player's is stale and ignored, so a tick still in flight
// across a restart cannot revive a second tick chain.
type tickMsg struct {
	gen int
}

// streamDoneMsg signals that the source is exhausted or has errored. A nil or
// [io.EOF]-family error is a normal end of stream.
type streamDoneMsg struct {
	err error
}

// Player is a [tea.Model] that plays a video in the terminal using ANSI
// half-block rendering. It opens a [Source] through a [SourceFunc], buffers a
// fraction of a second of frames before playback to absorb decode latency, and
// re-opens the source at the new size when the terminal is resized. Construct
// it with [NewPlayer].
//
// Frames are read ahead into memory faster than they are displayed, so Player
// is intended for short clips rather than arbitrarily long streams.
type Player struct {
	source    Source
	err       error
	newSource SourceFunc
	current   *image.RGBA
	resized   *image.RGBA
	pending   []*image.RGBA
	fps       int
	cols      int
	rows      int
	srcW      int
	srcH      int
	// Gen is the current playback epoch, bumped on every (re)open so that
	// ticks scheduled for an earlier source can be recognized as stale.
	gen     int
	loop    bool
	done    bool
	ticking bool
}

// Option configures a [Player].
type Option func(*Player)

// WithFPS sets the playback frame rate. Values below 1 are ignored.
func WithFPS(fps int) Option {
	return func(p *Player) {
		if fps >= 1 {
			p.fps = fps
		}
	}
}

// WithSize sets the initial terminal size in cells. The player adopts the real
// size as soon as the first [tea.WindowSizeMsg] arrives. Non-positive values
// are ignored.
func WithSize(cols, rows int) Option {
	return func(p *Player) {
		if cols > 0 {
			p.cols = cols
		}

		if rows > 0 {
			p.rows = rows
		}
	}
}

// WithLoop enables continuous looping: when set, playback restarts from the
// beginning once the source is exhausted.
func WithLoop(loop bool) Option {
	return func(p *Player) {
		p.loop = loop
	}
}

// NewPlayer creates a [Player] that opens its source through newSource. The
// source is opened when the program starts (see [Player.Init]); newSource is
// also called to re-open the source after a terminal resize or, with
// [WithLoop], when playback loops.
func NewPlayer(newSource SourceFunc, opts ...Option) *Player {
	p := &Player{
		newSource: newSource,
		fps:       defaultFPS,
		cols:      defaultCols,
		rows:      defaultRows,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Err returns the first unexpected error encountered during playback, or nil
// when playback ended normally. Read it after [tea.Program.Run] returns.
func (p *Player) Err() error {
	return p.err
}

// Init opens the source at the configured size and starts reading frames. It
// implements [tea.Model].
func (p *Player) Init() tea.Cmd {
	return p.openSource()
}

// Update advances playback in response to frames, ticks, resizes, and key
// presses. It implements [tea.Model].
func (p *Player) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return p.handleKey(msg)
	case tea.WindowSizeMsg:
		return p.handleResize(msg)
	case frameMsg:
		return p.handleFrame(msg)
	case streamDoneMsg:
		return p.handleDone(msg)
	case tickMsg:
		return p.handleTick(msg)
	}

	return p, nil
}

// View renders the current frame at the terminal size, scaling on demand. It
// implements [tea.Model].
func (p *Player) View() tea.View {
	if p.current == nil {
		v := tea.NewView("Loading...")
		v.AltScreen = true

		return v
	}

	if p.resized == nil {
		bounds := p.current.Bounds()
		if bounds.Dx() == p.cols && bounds.Dy() == p.rows*2 {
			p.resized = p.current
		} else {
			p.resized = Resize(p.current, p.cols, p.rows)
		}
	}

	v := tea.NewView(RenderFrame(p.resized, p.cols, p.rows))
	v.AltScreen = true

	return v
}

// handleKey quits on q, esc, or ctrl+c.
func (p *Player) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		p.closeSource()

		return p, tea.Quit
	}

	return p, nil
}

// handleResize adopts the new terminal size, re-opening the source only when
// the pixel dimensions actually change so a burst of resize events does not
// spawn redundant sources.
func (p *Player) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	p.cols = msg.Width
	p.rows = msg.Height

	if p.cols != p.srcW || p.rows*2 != p.srcH {
		cmd := p.restart(false)

		return p, cmd
	}

	// Same resolution: keep the source and re-scale on the next View.
	p.resized = nil

	return p, nil
}

// handleFrame buffers an incoming frame. The first frame is shown immediately;
// playback starts ticking once roughly half a second of frames is buffered, so
// decode latency does not eat into the tick budget.
func (p *Player) handleFrame(msg frameMsg) (tea.Model, tea.Cmd) {
	if p.current == nil {
		p.current = msg.frame

		cmd := p.readFrame()

		return p, cmd
	}

	p.pending = append(p.pending, msg.frame)

	if !p.ticking && len(p.pending) >= p.fps/2 {
		p.ticking = true

		return p, tea.Batch(p.readFrame(), p.tick())
	}

	cmd := p.readFrame()

	return p, cmd
}

// handleDone reacts to the source ending. An unexpected error (anything other
// than a clean end of stream) is recorded for Err and ends playback. A clean
// end during buffering starts ticking with whatever frames are buffered.
func (p *Player) handleDone(msg streamDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil &&
		!errors.Is(msg.err, io.EOF) &&
		!errors.Is(msg.err, io.ErrUnexpectedEOF) {
		p.err = msg.err

		return p, tea.Quit
	}

	p.done = true

	// The source ended before the first frame arrived; nothing to show.
	if p.current == nil {
		return p, tea.Quit
	}

	// Ended while still buffering: start ticking with the frames we have.
	if !p.ticking {
		p.ticking = true

		cmd := p.tick()

		return p, cmd
	}

	return p, nil
}

// handleTick advances to the next buffered frame. With nothing buffered it
// holds the current frame while the source is still producing, loops when the
// source is done and looping is enabled, and otherwise holds the last frame or
// quits when nothing was ever shown.
func (p *Player) handleTick(msg tickMsg) (tea.Model, tea.Cmd) {
	// A tick scheduled for a previous source survived a restart; let it die
	// rather than reschedule, so exactly one tick chain stays authoritative.
	if msg.gen != p.gen {
		return p, nil
	}

	if len(p.pending) > 0 {
		p.current = p.pending[0]
		p.pending = p.pending[1:]
		p.resized = nil

		cmd := p.tick()

		return p, cmd
	}

	if p.done {
		if p.loop {
			cmd := p.restart(true)

			return p, cmd
		}

		// Nothing left and not looping: hold the last frame, or quit if no
		// frame was ever shown.
		if p.current == nil {
			return p, tea.Quit
		}

		return p, nil
	}

	// The buffer is momentarily empty but the source is still producing; hold
	// the current frame and keep ticking.
	cmd := p.tick()

	return p, cmd
}

// openSource opens a new source at the current terminal size, records the
// pixel dimensions it was opened at, and returns a command that reads the
// first frame. A failure to open is delivered as a streamDoneMsg. It bumps the
// playback epoch so ticks scheduled for the previous source become stale.
func (p *Player) openSource() tea.Cmd {
	p.gen++

	pixW := p.cols
	pixH := p.rows * 2

	src, err := p.newSource(pixW, pixH)
	if err != nil {
		return func() tea.Msg {
			return streamDoneMsg{err: err}
		}
	}

	p.source = src
	p.srcW = pixW
	p.srcH = pixH

	return p.readFrame()
}

// restart stops the current source and opens a fresh one at the current size,
// resetting playback state so buffering begins again. When keepCurrent is true
// the on-screen frame is preserved so the view does not flash between loops
// while the new source buffers.
func (p *Player) restart(keepCurrent bool) tea.Cmd {
	p.closeSource()

	p.done = false
	p.ticking = false
	p.resized = nil
	p.pending = p.pending[:0]

	if !keepCurrent {
		p.current = nil
	}

	return p.openSource()
}

// closeSource closes the current source, if any.
func (p *Player) closeSource() {
	if p.source == nil {
		return
	}

	//nolint:errcheck,gosec // Closing a source we are abandoning has no actionable error.
	p.source.Close()

	p.source = nil
}

// readFrame returns a command that reads the next frame from the source open
// at call time. Binding the source into the closure keeps a read issued before
// a restart from consuming the replacement source.
func (p *Player) readFrame() tea.Cmd {
	src := p.source

	return func() tea.Msg {
		frame, err := src.ReadFrame()
		if err != nil {
			return streamDoneMsg{err: err}
		}

		return frameMsg{frame: frame}
	}
}

// tick returns a command that fires a tickMsg after one frame interval. The
// current epoch is captured so a tick still in flight after a restart is
// recognized as stale by handleTick.
func (p *Player) tick() tea.Cmd {
	gen := p.gen

	return tea.Tick(time.Second/time.Duration(p.fps), func(time.Time) tea.Msg {
		return tickMsg{gen: gen}
	})
}
