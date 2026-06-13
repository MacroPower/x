package ansivideo

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os/exec"
)

// ErrFFmpegNotFound indicates the ffmpeg executable is not on PATH.
var ErrFFmpegNotFound = errors.New("ffmpeg not found in PATH")

// FFmpegSource decodes a video file into raw RGBA frames by reading ffmpeg's
// rawvideo output, scaled and padded to the exact width x height pixel canvas
// requested at construction, so every frame [FFmpegSource.ReadFrame] returns
// has those dimensions.
type FFmpegSource struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	cancel context.CancelFunc
	width  int
	height int
}

// NewFFmpegSource starts ffmpeg to decode path at fps frames per second,
// scaling and padding the video to fit within a width x height pixel canvas
// while preserving its aspect ratio. It returns [ErrFFmpegNotFound] when
// ffmpeg is not on PATH. The caller must call [FFmpegSource.Close] to stop
// ffmpeg and release the pipe.
func NewFFmpegSource(
	ctx context.Context,
	path string,
	fps, width, height int,
) (*FFmpegSource, error) {
	_, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, ErrFFmpegNotFound
	}

	ctx, cancel := context.WithCancel(ctx)

	// Decode at fps, fit within the canvas without distorting the aspect
	// ratio, then pad the remainder so every frame is exactly width x height.
	filter := fmt.Sprintf(
		"fps=%d,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
		fps, width, height, width, height,
	)

	//nolint:gosec // path and the numeric filter are caller-provided, not untrusted input.
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", path,
		"-vf", filter,
		"-pix_fmt", "rgba",
		"-f", "rawvideo",
		"pipe:1",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()

		return nil, fmt.Errorf("opening ffmpeg stdout: %w", err)
	}

	err = cmd.Start()
	if err != nil {
		cancel()

		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}

	return &FFmpegSource{
		cmd:    cmd,
		stdout: stdout,
		cancel: cancel,
		width:  width,
		height: height,
	}, nil
}

// ReadFrame reads the next raw RGBA frame from ffmpeg. It returns [io.EOF], or
// [io.ErrUnexpectedEOF] on a truncated final frame, once ffmpeg has finished or
// been closed.
func (s *FFmpegSource) ReadFrame() (*image.RGBA, error) {
	buf := make([]byte, s.width*s.height*4)

	_, err := io.ReadFull(s.stdout, buf)

	// Return the end-of-stream sentinels unwrapped so they satisfy the
	// documented Source contract (and the io.Reader convention); wrap only a
	// genuine read failure.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, io.ErrUnexpectedEOF
	}

	if errors.Is(err, io.EOF) {
		return nil, io.EOF
	}

	if err != nil {
		return nil, fmt.Errorf("reading frame: %w", err)
	}

	return &image.RGBA{
		Pix:    buf,
		Stride: s.width * 4,
		Rect:   image.Rect(0, 0, s.width, s.height),
	}, nil
}

// Close stops ffmpeg and waits for it to exit. The non-zero exit ffmpeg
// reports on cancellation is expected and not returned.
func (s *FFmpegSource) Close() error {
	s.cancel()

	//nolint:errcheck,gosec // ffmpeg exits non-zero on cancellation; the wait error is expected.
	s.cmd.Wait()

	return nil
}

// FFmpeg returns a [SourceFunc] that decodes path at fps using ffmpeg, for use
// with [NewPlayer]. Each call opens a fresh [FFmpegSource] sized to the
// requested canvas, so the player can re-open the stream on resize and loop.
func FFmpeg(ctx context.Context, path string, fps int) SourceFunc {
	return func(width, height int) (Source, error) {
		return NewFFmpegSource(ctx, path, fps, width, height)
	}
}
