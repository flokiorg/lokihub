//go:build !darwin && !windows && !linux
package tray

func RenderIcon(_ []byte) ([]byte, error)        { return nil, nil }
func RenderIconFromPNG(_ []byte) ([]byte, error) { return nil, nil }
