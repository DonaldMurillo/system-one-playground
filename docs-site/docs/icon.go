package docs

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// DefaultIconPNG renders the same four-tab book as public/favicon.svg.
// Each tab identifies a repository component; the book unifies its documentation.
func DefaultIconPNG() []byte {
	canvas := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	rect := func(x, y, w, h int, c color.RGBA) {
		draw.Draw(canvas, image.Rect(x*4, y*4, (x+w)*4, (y+h)*4), &image.Uniform{C: c}, image.Point{}, draw.Src)
	}
	ink := color.RGBA{23, 28, 35, 255}
	paper := color.RGBA{242, 237, 225, 255}
	muted := color.RGBA{116, 125, 134, 255}
	amber := color.RGBA{232, 172, 88, 255}
	rect(0, 0, 64, 64, ink)
	rect(10, 15, 19, 34, paper)
	rect(35, 15, 19, 34, paper)
	rect(16, 9, 7, 13, color.RGBA{102, 198, 185, 255})
	rect(25, 9, 7, 13, color.RGBA{174, 155, 224, 255})
	rect(34, 9, 7, 13, amber)
	rect(43, 9, 7, 13, color.RGBA{127, 172, 222, 255})
	for _, x := range []int{15, 40} {
		for _, y := range []int{29, 37} {
			rect(x, y, 9, 3, muted)
		}
	}
	rect(10, 52, 44, 3, amber)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		panic(err)
	}
	return encoded.Bytes()
}
