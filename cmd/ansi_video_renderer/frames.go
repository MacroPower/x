package main

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// extractFrames shells out to ffmpeg to extract PNG frames from a video file.
// It returns the temporary directory containing the frames and a cleanup
// function that removes the directory.
func extractFrames(videoPath string, fps int) (string, func(), error) {
	_, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", nil, fmt.Errorf(
			"ffmpeg not found in PATH: install ffmpeg or use a directory of PNG frames instead",
		)
	}

	tmpDir, err := os.MkdirTemp("", "ansi_video_renderer_*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir: %w", err)
	}

	cleanup := func() { os.RemoveAll(tmpDir) }

	pattern := filepath.Join(tmpDir, "frame_%05d.png")

	//nolint:gosec // videoPath and fps are user-provided CLI arguments, not untrusted input.
	cmd := exec.CommandContext(
		context.Background(),
		"ffmpeg",
		"-i", videoPath,
		"-vf", fmt.Sprintf("fps=%d", fps),
		pattern,
	)

	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		cleanup()

		return "", nil, fmt.Errorf("running ffmpeg: %w", err)
	}

	return tmpDir, cleanup, nil
}

// loadFrames reads and decodes all PNG images from a directory, sorted by
// filename.
func loadFrames(dir string) ([]image.Image, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	var names []string

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		if strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			names = append(names, e.Name())
		}
	}

	slices.Sort(names)

	if len(names) == 0 {
		return nil, fmt.Errorf("no PNG files found in %s", dir)
	}

	frames := make([]image.Image, 0, len(names))

	for _, name := range names {
		img, err := decodePNG(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("decoding %s: %w", name, err)
		}

		frames = append(frames, img)
	}

	return frames, nil
}

func decodePNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer func() {
		closeErr := f.Close()
		if closeErr != nil {
			fmt.Fprintf(os.Stderr, "warning: closing %s: %v\n", path, closeErr)
		}
	}()

	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}

	return img, nil
}
