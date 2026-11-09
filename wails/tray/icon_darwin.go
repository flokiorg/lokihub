//go:build darwin

package tray

import (
	"image"
	"image/color"
)

// RenderIcon renders the SVG into a 36×36 template mask for the macOS menu bar.
// 36 px = 18 pt @2x — NSImage.size is set to (18,18) in Objective-C so the icon
// is Retina-sharp on high-DPI displays. The result is a black-on-transparent
// mask: createTray sets the NSImage as a template image, so macOS tints it to
// the bar (black in light mode, white in dark mode, highlighted when the menu
// is open). See inkMask for why the art is reduced to its ink coverage.
func RenderIcon(svg []byte) ([]byte, error) {
	img, err := renderSVG(svg, 36)
	if err != nil {
		return nil, err
	}
	return encodePNG(inkMask(img))
}

// RenderIconFromPNG scales the fallback PNG to 36×36 and reduces it to the same
// template mask as RenderIcon, so the fallback also tints to the menu bar.
func RenderIconFromPNG(data []byte) ([]byte, error) {
	img, err := scalePNG(data, 36)
	if err != nil {
		return nil, err
	}
	return encodePNG(inkMask(img))
}

// inkMask turns the two-tone tray art (dark ink lines over an opaque white fill
// over a transparent exterior) into a black-on-transparent template mask. A
// macOS template image keys off alpha only and ignores colour, so feeding it the
// art as-is would mask the whole opaque head and render a solid blob. Keying on
// ink coverage instead — alpha × (1 − luminance) — drops the white fill to
// transparent and keeps the line work, so the tinted icon reads as line art.
func inkMask(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			af := float64(a>>8) / 255.0
			lum := (299*float64(r>>8) + 587*float64(g>>8) + 114*float64(bl>>8)) / 1000.0 / 255.0
			ink := af * (1.0 - lum)
			dst.Set(x, y, color.RGBA{R: 0, G: 0, B: 0, A: uint8(ink*255.0 + 0.5)}) //nolint:gosec // ink is clamped to [0,1]
		}
	}
	return dst
}
