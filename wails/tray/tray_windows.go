//go:build windows

package tray

import (
	"runtime"

	"github.com/getlantern/systray"
)

func HideFromDock() {}
func ShowInDock()   {}

func Setup(title string, icon []byte, onShow func(), onQuit func()) {
	go func() {
		// Pin this goroutine to one OS thread for the lifetime of the message
		// pump. Windows requires that CreateWindow, GetMessage, DispatchMessage,
		// and TrackPopupMenu all execute on the same OS thread (thread affinity).
		// Without this, Go's async preemption can migrate the goroutine to a
		// different thread, causing GetMessage to stop receiving tray callbacks
		// and the right-click menu to silently never appear.
		runtime.LockOSThread()
		systray.Run(func() {
			if len(icon) > 0 {
				systray.SetIcon(icon)
			}
			systray.SetTitle(title)
			systray.SetTooltip(title)

			mShow := systray.AddMenuItem("Show Lokihub", "Show the Lokihub window")
			systray.AddSeparator()
			mQuit := systray.AddMenuItem("Quit", "Quit Lokihub")

			go func() {
				for {
					select {
					case <-mShow.ClickedCh:
						onShow()
					case <-mQuit.ClickedCh:
						systray.Quit()
						onQuit()
					}
				}
			}()
		}, func() {})
	}()
}
