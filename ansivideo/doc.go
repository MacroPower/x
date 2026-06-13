// Package ansivideo plays video in the terminal using ANSI-colored half-block
// characters.
//
// # Half-block rendering
//
// Each terminal cell encodes two vertically stacked pixels via the upper-half
// block glyph "▀": the cell's foreground color paints the top pixel and its
// background color paints the bottom pixel. A grid of cols x rows cells thus
// renders a cols x (rows*2) pixel canvas. [Resize] scales an image into that
// canvas (aspect-preserving, centered, black-padded) and [RenderFrame] turns a
// canvas into the truecolor escape sequences that draw it.
//
// # Frame sources
//
// A [Source] yields decoded frames one at a time. [FFmpegSource] is the
// built-in implementation; it shells out to ffmpeg to decode any format ffmpeg
// understands, scaling and padding each frame to the requested pixel canvas.
// [FFmpeg] adapts it to the [SourceFunc] factory that [Player] expects.
//
// # Playback
//
// [Player] is a [charm.land/bubbletea/v2.Model] that buffers a fraction of a
// second of frames to absorb decode latency, then advances at a fixed frame
// rate. It re-opens its source at the new size when the terminal is resized
// and, with [WithLoop], restarts when the source is exhausted. Frames are read
// ahead into memory, so the player is intended for short clips rather than
// arbitrarily long streams.
//
// # Usage
//
//	ctx := context.Background()
//	player := ansivideo.NewPlayer(
//		ansivideo.FFmpeg(ctx, "clip.mp4", 24),
//		ansivideo.WithFPS(24),
//		ansivideo.WithSize(cols, rows),
//	)
//
//	_, err := tea.NewProgram(player).Run()
//	if err == nil {
//		err = player.Err() // Surfaces an unexpected decode error, if any.
//	}
//
// ffmpeg must be installed and on PATH; [NewFFmpegSource] reports
// [ErrFFmpegNotFound] when it is not.
package ansivideo
