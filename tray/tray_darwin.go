//go:build darwin
package tray

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void createTray(const char* title, const void* iconData, int iconLen);
void hideFromDock(void);
void showInDock(void);
*/
import "C"
import "unsafe"

var showCb func()
var quitCb func()

//export goShowCallback
func goShowCallback() {
	if showCb != nil {
		showCb()
	}
}

//export goQuitCallback
func goQuitCallback() {
	if quitCb != nil {
		quitCb()
	}
}

func HideFromDock() { C.hideFromDock() }
func ShowInDock()   { C.showInDock() }

func Setup(title string, icon []byte, onShow func(), onQuit func()) {
	showCb = onShow
	quitCb = onQuit
	cTitle := C.CString(title)
	defer C.free(unsafe.Pointer(cTitle))

	var iconPtr unsafe.Pointer
	if len(icon) > 0 {
		iconPtr = unsafe.Pointer(&icon[0])
	}
	C.createTray(cTitle, iconPtr, C.int(len(icon)))
}
