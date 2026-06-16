//go:build !darwin && !windows && !linux
package tray

func HideFromDock() {}
func ShowInDock()   {}
func Setup(_ string, _ []byte, _ func(), _ func()) {}
