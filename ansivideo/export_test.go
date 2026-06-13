package ansivideo

import (
	"image"

	tea "charm.land/bubbletea/v2"
)

// This file exposes the player's internal messages and state to the external
// test package so the state machine can be driven deterministically, without
// real timers or sources. It is compiled only under test.

// NewFrameMsg builds the message delivered when a source produces a frame.
func NewFrameMsg(frame *image.RGBA) tea.Msg {
	return frameMsg{frame: frame}
}

// TickMsg builds the message delivered on each playback tick, stamped with the
// player's current epoch so it is processed rather than discarded as stale.
func (p *Player) TickMsg() tea.Msg {
	return tickMsg{gen: p.gen}
}

// StaleTickMsg builds a tick message stamped with an epoch that is guaranteed
// not to match the player's, so handleTick treats it as left over from a
// previous source.
func (p *Player) StaleTickMsg() tea.Msg {
	return tickMsg{gen: p.gen - 1}
}

// NewStreamDoneMsg builds the message delivered when a source ends or errors.
func NewStreamDoneMsg(err error) tea.Msg {
	return streamDoneMsg{err: err}
}

// PendingLen reports the number of buffered, not-yet-displayed frames.
func (p *Player) PendingLen() int {
	return len(p.pending)
}

// Ticking reports whether playback has started advancing frames.
func (p *Player) Ticking() bool {
	return p.ticking
}

// Done reports whether the current source has been fully consumed.
func (p *Player) Done() bool {
	return p.done
}

// HasCurrent reports whether a frame is currently on screen.
func (p *Player) HasCurrent() bool {
	return p.current != nil
}

// SourceDims reports the pixel dimensions the current source was opened at.
func (p *Player) SourceDims() (int, int) {
	return p.srcW, p.srcH
}
