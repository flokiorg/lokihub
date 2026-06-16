//go:build linux
package tray

// RenderIcon renders the SVG as a 32×32 full-colour PNG for the
// StatusNotifierItem IconPixmap property.
func RenderIcon(svg []byte) ([]byte, error) {
	img, err := renderSVG(svg, 32)
	if err != nil {
		return nil, err
	}
	return encodePNG(img)
}

// RenderIconFromPNG scales a PNG to 32×32 PNG for the StatusNotifierItem.
func RenderIconFromPNG(data []byte) ([]byte, error) {
	img, err := scalePNG(data, 32)
	if err != nil {
		return nil, err
	}
	return encodePNG(img)
}
