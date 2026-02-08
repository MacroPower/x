package main

import (
	"context"
	"fmt"
	"image"
	"io"
	"os/exec"

	tea "charm.land/bubbletea/v2"
)

// frameMsg carries a decoded frame from the ffmpeg pipe.
type frameMsg struct {
	frame *image.RGBA
}

// streamDoneMsg signals that the ffmpeg pipe has closed. A nil Err indicates
// normal EOF.
type streamDoneMsg struct {
	Err error
}

// frameStream manages an ffmpeg rawvideo pipe and delivers frames one at a
// time.
type frameStream struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	cancel context.CancelFunc
	pixW   int
	pixH   int
}

// newFrameStream starts ffmpeg to decode the given video file and pipe raw RGBA
// frames to stdout. The video is scaled and padded to fit exactly within pixW x
// pixH pixels using ffmpeg's built-in filters.
func newFrameStream(ctx context.Context, videoPath string, fps, pixW, pixH int) (*frameStream, error) {
	_, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf(
			"ffmpeg not found in PATH: install ffmpeg or use a directory of PNG frames instead",
		)
	}

	ctx, cancel := context.WithCancel(ctx)

	vf := fmt.Sprintf(
		"fps=%d,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
		fps, pixW, pixH, pixW, pixH,
	)

	//nolint:gosec // videoPath and fps are user-provided CLI arguments, not untrusted input.
	cmd := exec.CommandContext(
		ctx,
		"ffmpeg",
		"-i", videoPath,
		"-vf", vf,
		"-pix_fmt", "rgba",
		"-f", "rawvideo",
		"pipe:1",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()

		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	err = cmd.Start()
	if err != nil {
		cancel()

		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}

	return &frameStream{
		cmd:    cmd,
		stdout: stdout,
		cancel: cancel,
		pixW:   pixW,
		pixH:   pixH,
	}, nil
}

// readFrame returns a tea.Cmd that reads the next raw RGBA frame from the
// ffmpeg pipe. It returns a frameMsg on success or a streamDoneMsg when the
// pipe is exhausted or encounters an error.
func (fs *frameStream) readFrame() tea.Cmd {
	return func() tea.Msg {
		frameSize := fs.pixW * fs.pixH * 4

		buf := make([]byte, frameSize)

		_, err := io.ReadFull(fs.stdout, buf)
		if err != nil {
			return streamDoneMsg{Err: err}
		}

		frame := &image.RGBA{
			Pix:    buf,
			Stride: fs.pixW * 4,
			Rect:   image.Rect(0, 0, fs.pixW, fs.pixH),
		}

		return frameMsg{frame: frame}
	}
}

// stop cancels the ffmpeg process and waits for it to exit.
func (fs *frameStream) stop() {
	fs.cancel()
	//nolint:errcheck // Error is expected after context cancellation.
	fs.cmd.Wait()
}
