package main

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"golang.org/x/image/draw"
)

// resizeImage scales img to fit within cols x rows terminal cells (where each
// cell represents 2 vertical pixels via the half-block technique).
// The image is centered within the bounds and padded with black.
func resizeImage(img image.Image, cols, rows int) *image.RGBA {
	pixW := cols
	pixH := rows * 2

	dst := image.NewRGBA(image.Rect(0, 0, pixW, pixH))

	srcBounds := img.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	// Compute scale to fit within target while maintaining aspect ratio.
	scaleX := float64(pixW) / float64(srcW)
	scaleY := float64(pixH) / float64(srcH)

	scale := scaleX
	if scaleY < scaleX {
		scale = scaleY
	}

	newW := int(float64(srcW) * scale)
	newH := int(float64(srcH) * scale)

	// Center within the destination.
	offsetX := (pixW - newW) / 2
	offsetY := (pixH - newH) / 2

	dstRect := image.Rect(offsetX, offsetY, offsetX+newW, offsetY+newH)
	draw.ApproxBiLinear.Scale(dst, dstRect, img, srcBounds, draw.Over, nil)

	return dst
}

// renderFrame writes ANSI-styled half-block characters for the given image to
// the provided builder.
// Each terminal row represents 2 vertical pixels: the top pixel is the
// foreground color and the bottom pixel is the background color of a "▀"
// character.
func renderFrame(img *image.RGBA, cols, rows int, w *strings.Builder) {
	w.Reset()

	pixH := img.Bounds().Dy()

	for row := range rows {
		topY := row * 2
		botY := topY + 1

		for x := range cols {
			top := img.RGBAAt(x, topY)

			var bot color.RGBA
			if botY < pixH {
				bot = img.RGBAAt(x, botY)
			}

			fmt.Fprintf(w, "\033[38;2;%d;%d;%dm\033[48;2;%d;%d;%dm▀", top.R, top.G, top.B, bot.R, bot.G, bot.B)
		}

		w.WriteString("\033[0m\n")
	}
}
