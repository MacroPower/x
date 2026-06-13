package ansivideo_test

import (
	"errors"
	"image"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tea "charm.land/bubbletea/v2"

	"go.jacobcolvin.com/x/ansivideo"
)

// fakeSource is an in-memory [ansivideo.Source] used to observe the player's
// lifecycle. It delivers its frames in order and then reports [io.EOF]. Tests
// that drive the player with synthetic messages leave frames empty; tests that
// execute the player's read commands seed it.
type fakeSource struct {
	frames []*image.RGBA
	idx    int
	closed bool
}

func (s *fakeSource) ReadFrame() (*image.RGBA, error) {
	if s.idx >= len(s.frames) {
		return nil, io.EOF
	}

	frame := s.frames[s.idx]
	s.idx++

	return frame, nil
}

func (s *fakeSource) Close() error {
	s.closed = true

	return nil
}

// recordingFactory is a [ansivideo.SourceFunc] that records how often it is
// called and the dimensions of its most recent open. With err set, every open
// fails with that error instead of returning a source.
type recordingFactory struct {
	err     error
	sources []*fakeSource
	opens   int
	width   int
	height  int
}

func (f *recordingFactory) open(width, height int) (ansivideo.Source, error) {
	f.opens++
	f.width = width
	f.height = height

	if f.err != nil {
		return nil, f.err
	}

	src := &fakeSource{}
	f.sources = append(f.sources, src)

	return src, nil
}

// blankFrame returns a small dummy frame for tests that buffer frames without
// rendering them.
func blankFrame() *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, 4, 4))
}

func TestPlayerInitOpensSourceAtConfiguredSize(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithSize(20, 10))

	p.Init()

	assert.Equal(t, 1, f.opens)
	assert.Equal(t, 20, f.width)
	assert.Equal(t, 20, f.height, "pixel height is rows*2")

	w, h := p.SourceDims()
	assert.Equal(t, 20, w)
	assert.Equal(t, 20, h)
}

func TestPlayerShowsFirstFrameImmediately(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithFPS(10), ansivideo.WithSize(4, 2))

	p.Init()
	assert.False(t, p.HasCurrent())

	p.Update(ansivideo.NewFrameMsg(blankFrame()))

	assert.True(t, p.HasCurrent())
	assert.False(t, p.Ticking(), "one frame is not enough to start playback")
	assert.Zero(t, p.PendingLen(), "the first frame is shown, not buffered")
}

func TestPlayerStartsTickingAfterWarmup(t *testing.T) {
	t.Parallel()

	const fps = 8 // Warmup threshold is fps/2 buffered frames.

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithFPS(fps), ansivideo.WithSize(4, 2))

	p.Init()
	p.Update(ansivideo.NewFrameMsg(blankFrame())) // Becomes the current frame.

	for range fps/2 - 1 {
		p.Update(ansivideo.NewFrameMsg(blankFrame()))
	}

	assert.False(t, p.Ticking(), "still one frame short of the warmup threshold")
	assert.Equal(t, fps/2-1, p.PendingLen())

	p.Update(ansivideo.NewFrameMsg(blankFrame()))

	assert.True(t, p.Ticking(), "reaching the threshold starts playback")
}

func TestPlayerTickAdvancesBufferedFrames(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithFPS(2), ansivideo.WithSize(4, 2))

	p.Init()
	p.Update(ansivideo.NewFrameMsg(blankFrame())) // Current frame.
	p.Update(ansivideo.NewFrameMsg(blankFrame())) // Buffered; fps/2 == 1 starts ticking.

	require.True(t, p.Ticking())
	require.Equal(t, 1, p.PendingLen())

	p.Update(p.TickMsg())

	assert.Zero(t, p.PendingLen(), "the tick consumed the buffered frame")
}

func TestPlayerLoopsWhenDone(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open,
		ansivideo.WithFPS(4),
		ansivideo.WithSize(4, 2),
		ansivideo.WithLoop(true),
	)

	p.Init()
	require.Equal(t, 1, f.opens)

	first := f.sources[0]

	p.Update(ansivideo.NewFrameMsg(blankFrame()))
	p.Update(ansivideo.NewStreamDoneMsg(io.EOF)) // Done with a frame shown; starts ticking.
	require.True(t, p.Done())
	require.True(t, p.Ticking())

	_, cmd := p.Update(p.TickMsg()) // Empty buffer + done + loop -> restart.

	assert.NotNil(t, cmd)
	assert.Equal(t, 2, f.opens, "looping reopens the source")
	assert.True(t, first.closed, "the old source is closed on restart")
	assert.False(t, p.Done(), "restart resets the done flag")
	assert.True(t, p.HasCurrent(), "looping keeps the last frame to avoid a flash")
}

func TestPlayerHoldsLastFrameWhenDoneWithoutLoop(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithFPS(4), ansivideo.WithSize(4, 2))

	p.Init()
	p.Update(ansivideo.NewFrameMsg(blankFrame()))
	p.Update(ansivideo.NewStreamDoneMsg(io.EOF))
	require.True(t, p.Ticking())

	_, cmd := p.Update(p.TickMsg())

	assert.Nil(t, cmd, "holding the last frame issues no further commands")
	assert.True(t, p.HasCurrent())
}

func TestPlayerRecordsUnexpectedErrorAndQuits(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithSize(4, 2))

	p.Init()

	wantErr := errors.New("decode failure")
	_, cmd := p.Update(ansivideo.NewStreamDoneMsg(wantErr))

	require.NotNil(t, cmd)
	assert.Equal(t, tea.QuitMsg{}, cmd(), "an unexpected error quits the program")
	assert.ErrorIs(t, p.Err(), wantErr)
}

func TestPlayerQuitsWhenSourceEndsWithNoFrames(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithSize(4, 2))

	p.Init()

	_, cmd := p.Update(ansivideo.NewStreamDoneMsg(io.EOF))

	require.NotNil(t, cmd)
	assert.Equal(t, tea.QuitMsg{}, cmd())
	assert.NoError(t, p.Err(), "a clean EOF is not an error")
}

func TestPlayerResizeReopensSourceOnlyWhenSizeChanges(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithFPS(4), ansivideo.WithSize(10, 5))

	p.Init()
	require.Equal(t, 1, f.opens)

	first := f.sources[0]

	// Same pixel dimensions as opened (10 cols, 5 rows -> 10x10 px): no reopen.
	p.Update(tea.WindowSizeMsg{Width: 10, Height: 5})
	assert.Equal(t, 1, f.opens, "an unchanged size does not reopen the source")
	assert.False(t, first.closed)

	// A different size reopens at the new pixel dimensions.
	p.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
	assert.Equal(t, 2, f.opens, "a size change reopens the source")
	assert.True(t, first.closed, "the old source is closed on a resize restart")

	w, h := p.SourceDims()
	assert.Equal(t, 20, w)
	assert.Equal(t, 16, h)
}

func TestPlayerQuitsOnQuitKeys(t *testing.T) {
	t.Parallel()

	tests := map[string]tea.KeyPressMsg{
		"q":      {Text: "q", Code: 'q'},
		"esc":    {Code: tea.KeyEscape},
		"ctrl+c": {Code: 'c', Mod: tea.ModCtrl},
	}

	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := &recordingFactory{}
			p := ansivideo.NewPlayer(f.open, ansivideo.WithSize(4, 2))

			p.Init()

			_, cmd := p.Update(key)

			require.NotNil(t, cmd)
			assert.Equal(t, tea.QuitMsg{}, cmd())
			assert.True(t, f.sources[0].closed, "quitting closes the source")
		})
	}
}

func TestPlayerIgnoresOtherKeys(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithSize(4, 2))

	p.Init()

	_, cmd := p.Update(tea.KeyPressMsg{Text: "a", Code: 'a'})

	assert.Nil(t, cmd)
	assert.False(t, f.sources[0].closed)
}

func TestPlayerViewShowsLoadingThenFrame(t *testing.T) {
	t.Parallel()

	const (
		cols = 3
		rows = 2
	)

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithFPS(4), ansivideo.WithSize(cols, rows))

	p.Init()

	loading := p.View()
	assert.True(t, loading.AltScreen)
	assert.Equal(t, "Loading...", loading.Content)

	frame := image.NewRGBA(image.Rect(0, 0, cols, rows*2))
	p.Update(ansivideo.NewFrameMsg(frame))

	rendered := p.View()
	assert.True(t, rendered.AltScreen)
	assert.Equal(t, ansivideo.RenderFrame(frame, cols, rows), rendered.Content,
		"a frame at the exact size renders without re-scaling")
}

func TestPlayerViewResizesMismatchedFrame(t *testing.T) {
	t.Parallel()

	const (
		cols = 4
		rows = 2
	)

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithSize(cols, rows))

	p.Init()

	// A frame larger than the cell grid forces a re-scale in View.
	frame := image.NewRGBA(image.Rect(0, 0, 20, 20))
	p.Update(ansivideo.NewFrameMsg(frame))

	rendered := p.View()

	want := ansivideo.RenderFrame(ansivideo.Resize(frame, cols, rows), cols, rows)
	assert.Equal(t, want, rendered.Content)
}

func TestPlayerReadCommandDeliversFramesThenEnd(t *testing.T) {
	t.Parallel()

	src := &fakeSource{frames: []*image.RGBA{blankFrame()}}
	open := func(_, _ int) (ansivideo.Source, error) { return src, nil }
	p := ansivideo.NewPlayer(open, ansivideo.WithSize(4, 2))

	// Init opens the source and returns a read command; executing it delivers
	// the seeded frame as a message.
	read := p.Init()
	require.NotNil(t, read)

	_, next := p.Update(read())
	require.NotNil(t, next)
	assert.True(t, p.HasCurrent(), "the delivered frame becomes current")

	// The next read exhausts the source and reports the stream done.
	p.Update(next())
	assert.True(t, p.Done())
}

func TestPlayerReportsSourceOpenError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("open failure")
	f := &recordingFactory{err: wantErr}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithSize(4, 2))

	// Init cannot open the source; the returned command reports the failure,
	// which the player records and quits on.
	open := p.Init()
	require.NotNil(t, open)
	require.Equal(t, 1, f.opens)

	_, cmd := p.Update(open())

	require.NotNil(t, cmd)
	assert.Equal(t, tea.QuitMsg{}, cmd())
	assert.ErrorIs(t, p.Err(), wantErr)
}

func TestPlayerHoldsCurrentFrameOnBufferUnderrun(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithFPS(2), ansivideo.WithSize(4, 2))

	p.Init()
	p.Update(ansivideo.NewFrameMsg(blankFrame())) // Current frame.
	p.Update(ansivideo.NewFrameMsg(blankFrame())) // Buffered; fps/2 == 1 starts ticking.

	p.Update(p.TickMsg()) // Drains the buffer; pending now empty, source still producing.
	require.Zero(t, p.PendingLen())
	require.False(t, p.Done())

	_, cmd := p.Update(p.TickMsg()) // Underrun: hold the current frame and keep ticking.

	assert.NotNil(t, cmd, "an underrun keeps ticking rather than stalling")
	assert.True(t, p.HasCurrent(), "the current frame is held")
	assert.False(t, p.Done())
}

func TestPlayerDiscardsStaleTickAfterRestart(t *testing.T) {
	t.Parallel()

	f := &recordingFactory{}
	p := ansivideo.NewPlayer(f.open, ansivideo.WithFPS(4), ansivideo.WithSize(10, 5))

	p.Init()

	// Capture a tick for the current epoch, then force a restart by resizing.
	stale := p.TickMsg()
	p.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
	require.Equal(t, 2, f.opens)

	_, cmd := p.Update(stale)

	assert.Nil(t, cmd, "a tick scheduled before a restart does not reschedule a second chain")
}

func TestPlayerOptionGuardsKeepDefaults(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		opts     []ansivideo.Option
		wantW    int
		wantH    int
		wantWarm int // Buffered frames needed to start ticking (defaultFPS/2).
	}{
		"zero values": {
			opts:     []ansivideo.Option{ansivideo.WithFPS(0), ansivideo.WithSize(0, 0)},
			wantW:    80,
			wantH:    48,
			wantWarm: 12,
		},
		"negative values": {
			opts:     []ansivideo.Option{ansivideo.WithFPS(-5), ansivideo.WithSize(-1, -1)},
			wantW:    80,
			wantH:    48,
			wantWarm: 12,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := &recordingFactory{}
			p := ansivideo.NewPlayer(f.open, tc.opts...)

			p.Init()

			w, h := p.SourceDims()
			assert.Equal(t, tc.wantW, w, "non-positive size keeps the default columns")
			assert.Equal(t, tc.wantH, h, "non-positive size keeps the default rows*2")

			// The default fps survives, so warmup needs defaultFPS/2 buffered frames.
			p.Update(ansivideo.NewFrameMsg(blankFrame())) // Current frame.

			for range tc.wantWarm - 1 {
				p.Update(ansivideo.NewFrameMsg(blankFrame()))
			}

			assert.False(t, p.Ticking(), "one short of the default warmup threshold")

			p.Update(ansivideo.NewFrameMsg(blankFrame()))
			assert.True(t, p.Ticking(), "the default fps drives the warmup threshold")
		})
	}
}
