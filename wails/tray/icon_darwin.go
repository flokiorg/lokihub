//go:build darwin

package tray

import (
	"image"
	"image/color"

	"golang.org/x/image/draw"
)

const (
	// trayPx is the final menu-bar mask size: 36 px = 18 pt @2x (NSImage.size is
	// (18,18) in Objective-C), so the icon is Retina-sharp on high-DPI displays.
	trayPx = 36
	// superSample rasterises the art larger, then downscales, so the line work is
	// anti-aliased (a direct 36 px raster is jagged) and the bold dilation below
	// can thicken by a sub-pixel amount instead of a whole pixel.
	superSample = 4
	// dilateR thickens the ink by dilateR px at the supersampled size, i.e.
	// dilateR/superSample ≈ 0.5 px at the final size — a slightly bolder weight
	// that stays legible, where a full final-size pixel of dilation would merge
	// the fine face/helmet detail into a blob.
	dilateR = 2
)

// RenderIcon renders the SVG into a 36×36 template mask for the macOS menu bar.
// 36 px = 18 pt @2x — NSImage.size is set to (18,18) in Objective-C so the icon
// is Retina-sharp on high-DPI displays. The result is a black-on-transparent
// mask: createTray sets the NSImage as a template image, so macOS tints it to
// the bar (black in light mode, white in dark mode, highlighted when the menu
// is open). See inkMask for why the art is reduced to its ink coverage, and
// boldMask for the supersample + slight thickening that makes it read bolder.
func RenderIcon(svg []byte) ([]byte, error) {
	img, err := renderSVG(svg, trayPx*superSample)
	if err != nil {
		return nil, err
	}
	return encodePNG(boldMask(img))
}

// RenderIconFromPNG scales the fallback PNG to the supersampled size and reduces
// it to the same template mask as RenderIcon, so the fallback also tints to the
// menu bar.
func RenderIconFromPNG(data []byte) ([]byte, error) {
	img, err := scalePNG(data, trayPx*superSample)
	if err != nil {
		return nil, err
	}
	return encodePNG(boldMask(img))
}

// boldMask reduces the supersampled art to its ink mask, thickens that mask by a
// sub-pixel amount so the line art reads bolder in the menu bar, then downscales
// to the final trayPx×trayPx with high-quality resampling.
func boldMask(src *image.RGBA) *image.RGBA {
	mask := dilateAlpha(inkMask(src), dilateR)
	dst := image.NewRGBA(image.Rect(0, 0, trayPx, trayPx))
	draw.CatmullRom.Scale(dst, dst.Bounds(), mask, mask.Bounds(), draw.Over, nil)
	return dst
}

// dilateAlpha grows the mask's alpha (grayscale dilation): each pixel takes the
// maximum alpha in its (2r+1)² neighbourhood, thickening the black ink. The
// colour stays black; only coverage grows.
func dilateAlpha(src *image.RGBA, r int) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			var maxA uint8
			for dy := -r; dy <= r; dy++ {
				yy := y + dy
				if yy < b.Min.Y || yy >= b.Max.Y {
					continue
				}
				for dx := -r; dx <= r; dx++ {
					xx := x + dx
					if xx < b.Min.X || xx >= b.Max.X {
						continue
					}
					if a := src.RGBAAt(xx, yy).A; a > maxA {
						maxA = a
					}
				}
			}
			dst.Set(x, y, color.RGBA{R: 0, G: 0, B: 0, A: maxA})
		}
	}
	return dst
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
