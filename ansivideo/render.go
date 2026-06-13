package ansivideo

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"golang.org/x/image/draw"
)

// upperHalfBlock is the glyph every cell renders. Its foreground color paints
// the top pixel of the cell and its background color paints the bottom pixel,
// so one cell encodes two vertically stacked pixels.
const upperHalfBlock = "▀"

// cellBytes is a rough upper bound on the bytes one cell contributes (two
// truecolor escapes plus the glyph), used only to pre-size the render buffer.
const cellBytes = 40

// rowResetBytes is the byte length of the per-row style reset and newline.
const rowResetBytes = len("\x1b[0m\n")

// Resize scales img to fit within a cols x rows grid of terminal cells, where
// each cell stacks two vertical pixels, producing a cols x (rows*2) pixel
// canvas. The image keeps its aspect ratio, is centered within the canvas, and
// the surrounding margin is left black. An empty source image yields a fully
// black canvas.
func Resize(img image.Image, cols, rows int) *image.RGBA {
	pixW := cols
	pixH := rows * 2

	dst := image.NewRGBA(image.Rect(0, 0, pixW, pixH))

	src := img.Bounds()
	srcW := src.Dx()
	srcH := src.Dy()

	if srcW <= 0 || srcH <= 0 {
		return dst
	}

	// Scale to fit within the canvas without distorting the aspect ratio.
	scale := float64(pixW) / float64(srcW)
	if sy := float64(pixH) / float64(srcH); sy < scale {
		scale = sy
	}

	newW := int(float64(srcW) * scale)
	newH := int(float64(srcH) * scale)

	// Center the scaled image within the canvas.
	offsetX := (pixW - newW) / 2
	offsetY := (pixH - newH) / 2

	rect := image.Rect(offsetX, offsetY, offsetX+newW, offsetY+newH)
	draw.ApproxBiLinear.Scale(dst, rect, img, src, draw.Over, nil)

	return dst
}

// RenderFrame returns the ANSI escape sequences that draw img across a cols x
// rows grid of terminal cells using the half-block technique: each cell's
// foreground color is the top pixel and its background color is the bottom
// pixel. Cells whose bottom pixel lies past the image height render that pixel
// black. Every row ends with a style reset and a newline.
func RenderFrame(img *image.RGBA, cols, rows int) string {
	var sb strings.Builder

	sb.Grow(rows * (cols*cellBytes + rowResetBytes))

	bounds := img.Bounds()
	pixH := bounds.Dy()

	for row := range rows {
		topY := row * 2
		botY := topY + 1

		for x := range cols {
			top := img.RGBAAt(bounds.Min.X+x, bounds.Min.Y+topY)

			var bot color.RGBA

			if botY < pixH {
				bot = img.RGBAAt(bounds.Min.X+x, bounds.Min.Y+botY)
			}

			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm%s",
				top.R, top.G, top.B, bot.R, bot.G, bot.B, upperHalfBlock)
		}

		sb.WriteString("\x1b[0m\n")
	}

	return sb.String()
}
