package ansivideo

import "image"

// Source produces decoded video frames, one per call to [Source.ReadFrame],
// until the stream is exhausted. Implementations are not required to be safe
// for concurrent use.
type Source interface {
	// ReadFrame returns the next frame. It returns [io.EOF] once the stream is
	// exhausted, and any other error on failure.
	ReadFrame() (*image.RGBA, error)

	// Close releases the resources held by the source. ReadFrame must not be
	// called after Close.
	Close() error
}

// SourceFunc opens a [Source] that decodes into a width x height pixel canvas.
// [Player] calls it to open its source on start and to re-open the source at a
// new size after a terminal resize or, with [WithLoop], when playback loops.
// Returning an error aborts playback with that error.
type SourceFunc func(width, height int) (Source, error)
