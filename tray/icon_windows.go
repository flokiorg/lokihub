//go:build windows
package tray

import (
	"bytes"
	"encoding/binary"
)

// RenderIcon renders the SVG as a 32×32 ICO (PNG-inside-ICO).
// getlantern/systray on Windows requires ICO format.
func RenderIcon(svg []byte) ([]byte, error) {
	img, err := renderSVG(svg, 32)
	if err != nil {
		return nil, err
	}
	pngBytes, err := encodePNG(img)
	if err != nil {
		return nil, err
	}
	return wrapICO(pngBytes, 32, 32), nil
}

// RenderIconFromPNG scales a PNG to 32×32 and wraps it in ICO format.
// Used as a fallback when SVG rendering is unavailable.
func RenderIconFromPNG(data []byte) ([]byte, error) {
	img, err := scalePNG(data, 32)
	if err != nil {
		return nil, err
	}
	pngBytes, err := encodePNG(img)
	if err != nil {
		return nil, err
	}
	return wrapICO(pngBytes, 32, 32), nil
}

// wrapICO wraps a PNG into a single-image ICO container.
// Windows Vista+ supports PNG-in-ICO natively.
func wrapICO(pngData []byte, w, h int) []byte {
	const headerSize = 6
	const entrySize = 16
	offset := uint32(headerSize + entrySize)

	buf := new(bytes.Buffer)
	// ICO file header
	binary.Write(buf, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(buf, binary.LittleEndian, uint16(1)) // type = ICO
	binary.Write(buf, binary.LittleEndian, uint16(1)) // image count
	// Image directory entry
	buf.Write([]byte{byte(w), byte(h), 0, 0})                        // w, h, colorcount, reserved
	binary.Write(buf, binary.LittleEndian, uint16(1))                 // planes
	binary.Write(buf, binary.LittleEndian, uint16(32))                // bit depth
	binary.Write(buf, binary.LittleEndian, uint32(len(pngData)))      // data size
	binary.Write(buf, binary.LittleEndian, offset)                    // data offset
	// Image data
	buf.Write(pngData)
	return buf.Bytes()
}
