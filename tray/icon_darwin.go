//go:build darwin
package tray

// RenderIcon renders the SVG as a 36×36 full-colour PNG.
// 36 px = 18 pt @2x — NSImage.size is set to (18,18) in Objective-C so
// the icon is Retina-sharp on high-DPI displays.
func RenderIcon(svg []byte) ([]byte, error) {
	img, err := renderSVG(svg, 36)
	if err != nil {
		return nil, err
	}
	return encodePNG(img)
}

// RenderIconFromPNG scales a PNG to 36×36 PNG for the macOS menu bar.
func RenderIconFromPNG(data []byte) ([]byte, error) {
	img, err := scalePNG(data, 36)
	if err != nil {
		return nil, err
	}
	return encodePNG(img)
}
