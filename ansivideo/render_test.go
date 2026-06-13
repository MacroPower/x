package ansivideo_test

import (
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/ansivideo"
)

// cell returns the escape sequence RenderFrame emits for one cell: a truecolor
// foreground (top pixel), a truecolor background (bottom pixel), and the glyph.
func cell(top, bot color.RGBA) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀",
		top.R, top.G, top.B, bot.R, bot.G, bot.B)
}

func TestResizeProducesCanvasBounds(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		srcW int
		srcH int
		cols int
		rows int
		want image.Rectangle
	}{
		"square into wide": {srcW: 10, srcH: 10, cols: 4, rows: 4, want: image.Rect(0, 0, 4, 8)},
		"wide into square": {srcW: 16, srcH: 9, cols: 8, rows: 8, want: image.Rect(0, 0, 8, 16)},
		"single cell":      {srcW: 4, srcH: 4, cols: 1, rows: 1, want: image.Rect(0, 0, 1, 2)},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := image.NewRGBA(image.Rect(0, 0, tc.srcW, tc.srcH))
			got := ansivideo.Resize(src, tc.cols, tc.rows)

			assert.Equal(t, tc.want, got.Bounds())
		})
	}
}

func TestResizeCentersAndPads(t *testing.T) {
	t.Parallel()

	// A 10x10 solid-red square scaled into a 4x4 cell grid (4x8 px canvas):
	// the aspect ratio forces a 4x4 scaled image centered vertically, leaving
	// two black rows above and below.
	src := image.NewRGBA(image.Rect(0, 0, 10, 10))

	red := color.RGBA{R: 255, G: 0, B: 0, A: 255}
	for y := range 10 {
		for x := range 10 {
			src.SetRGBA(x, y, red)
		}
	}

	got := ansivideo.Resize(src, 4, 4)

	assert.Equal(t, image.Rect(0, 0, 4, 8), got.Bounds())
	assert.Zero(t, got.RGBAAt(0, 0).R, "top margin should be black")
	assert.Zero(t, got.RGBAAt(0, 7).R, "bottom margin should be black")
	assert.Equal(t, uint8(255), got.RGBAAt(0, 3).R, "center should keep the image color")
	assert.Equal(t, uint8(255), got.RGBAAt(3, 4).R, "center should keep the image color")
}

func TestResizeCentersTallImageHorizontally(t *testing.T) {
	t.Parallel()

	// A 4x16 portrait source into a 8x2 cell grid (8x4 px canvas) is
	// height-limited, so it scales to a 1x4 column centered horizontally with
	// black left and right margins.
	src := image.NewRGBA(image.Rect(0, 0, 4, 16))

	red := color.RGBA{R: 255, G: 0, B: 0, A: 255}
	for y := range 16 {
		for x := range 4 {
			src.SetRGBA(x, y, red)
		}
	}

	got := ansivideo.Resize(src, 8, 2)

	assert.Equal(t, image.Rect(0, 0, 8, 4), got.Bounds())
	assert.Zero(t, got.RGBAAt(0, 0).R, "left margin should be black")
	assert.Zero(t, got.RGBAAt(7, 3).R, "right margin should be black")
	assert.Equal(t, uint8(255), got.RGBAAt(3, 0).R, "center column should keep the image color")
	assert.Equal(t, uint8(255), got.RGBAAt(3, 3).R, "center column should keep the image color")
}

func TestResizeEmptyImageIsBlackCanvas(t *testing.T) {
	t.Parallel()

	src := image.NewRGBA(image.Rect(0, 0, 0, 0))
	got := ansivideo.Resize(src, 3, 2)

	assert.Equal(t, image.Rect(0, 0, 3, 4), got.Bounds())
	assert.Equal(t, color.RGBA{}, got.RGBAAt(0, 0))
}

func TestRenderFrame(t *testing.T) {
	t.Parallel()

	var (
		c00 = color.RGBA{R: 255, A: 255}
		c10 = color.RGBA{G: 255, A: 255}
		c01 = color.RGBA{B: 255, A: 255}
		c11 = color.RGBA{R: 255, G: 255, A: 255}
		c02 = color.RGBA{R: 255, B: 255, A: 255}
		c12 = color.RGBA{G: 255, B: 255, A: 255}
		c03 = color.RGBA{R: 128, G: 128, B: 128, A: 255}
		c13 = color.RGBA{R: 10, G: 20, B: 30, A: 255}
	)

	img := image.NewRGBA(image.Rect(0, 0, 2, 4))
	img.SetRGBA(0, 0, c00)
	img.SetRGBA(1, 0, c10)
	img.SetRGBA(0, 1, c01)
	img.SetRGBA(1, 1, c11)
	img.SetRGBA(0, 2, c02)
	img.SetRGBA(1, 2, c12)
	img.SetRGBA(0, 3, c03)
	img.SetRGBA(1, 3, c13)

	// Each row pairs the top pixel (foreground) with the pixel below it
	// (background), then resets the style. LinesLF terminates each row with the
	// newline RenderFrame writes.
	want := stringtest.LinesLF(
		cell(c00, c01)+cell(c10, c11)+"\x1b[0m",
		cell(c02, c03)+cell(c12, c13)+"\x1b[0m",
	)

	assert.Equal(t, want, ansivideo.RenderFrame(img, 2, 2))
}

func TestRenderFrameFillsMissingBottomPixelWithBlack(t *testing.T) {
	t.Parallel()

	// A 1x1 image rendered into a 1x1 cell has no bottom pixel, which renders
	// black.
	top := color.RGBA{R: 200, G: 100, B: 50, A: 255}
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, top)

	want := stringtest.LinesLF(cell(top, color.RGBA{}) + "\x1b[0m")

	assert.Equal(t, want, ansivideo.RenderFrame(img, 1, 1))
}
