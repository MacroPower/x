package ansivideo_test

import (
	"image"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/ansivideo"
)

//nolint:paralleltest // t.Setenv is incompatible with t.Parallel.
func TestNewFFmpegSourceReportsMissingFFmpeg(t *testing.T) {
	// An empty PATH guarantees ffmpeg cannot be found.
	t.Setenv("PATH", t.TempDir())

	_, err := ansivideo.NewFFmpegSource(t.Context(), "clip.mp4", 24, 16, 16)

	require.ErrorIs(t, err, ansivideo.ErrFFmpegNotFound)
}

func TestIntegrationFFmpegSourceDecodesFrames(t *testing.T) {
	t.Parallel()

	_, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}

	clip := filepath.Join(t.TempDir(), "clip.avi")

	gen := exec.CommandContext(t.Context(),
		"ffmpeg", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=32x32:rate=10",
		"-c:v", "mpeg4", "-q:v", "5", clip,
	)

	out, err := gen.CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg could not generate a test clip: %v: %s", err, out)
	}

	const (
		width  = 16
		height = 16
	)

	// Exercise the SourceFunc factory the player uses, not just the
	// constructor.
	open := ansivideo.FFmpeg(t.Context(), clip, 10)

	src, err := open(width, height)
	require.NoError(t, err)

	defer func() {
		assert.NoError(t, src.Close())
	}()

	frames := 0

	for {
		frame, readErr := src.ReadFrame()
		if readErr != nil {
			require.ErrorIs(t, readErr, io.EOF, "decoding should end with a clean EOF")

			break
		}

		assert.Equal(t, image.Rect(0, 0, width, height), frame.Bounds())

		frames++
	}

	assert.Positive(t, frames, "expected at least one decoded frame")
}
