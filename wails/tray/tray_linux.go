//go:build linux

package tray

// Linux system tray via the StatusNotifierItem D-Bus spec.
// This avoids calling gtk_main() so it never conflicts with Wails' WebKit2GTK event loop.
// Supported by KDE Plasma, XFCE, and GNOME with the AppIndicator shell extension.

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png"
	"os"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

const (
	sniPath     = dbus.ObjectPath("/StatusNotifierItem")
	menuPath    = dbus.ObjectPath("/Menu")
	sniIface    = "org.kde.StatusNotifierItem"
	menuIface   = "com.canonical.dbusmenu"
	watcherSvc  = "org.kde.StatusNotifierWatcher"
	watcherPath = dbus.ObjectPath("/StatusNotifierWatcher")
)

// pixmap holds one size of icon in ARGB32 big-endian format, as required by the spec.
type pixmap struct {
	W int32
	H int32
	D []byte
}

type tooltip struct {
	IconName string
	Pixmaps  []pixmap
	Title    string
	Body     string
}

// sniMethods handles org.kde.StatusNotifierItem method calls.
type sniMethods struct {
	onShow func()
	onQuit func()
}

func (s *sniMethods) Activate(x, y int32) *dbus.Error {
	go s.onShow()
	return nil
}

func (s *sniMethods) SecondaryActivate(x, y int32) *dbus.Error {
	go s.onShow()
	return nil
}

func (s *sniMethods) ContextMenu(x, y int32) *dbus.Error                 { return nil }
func (s *sniMethods) Scroll(delta int32, orientation string) *dbus.Error { return nil }

// menuNode matches the D-Bus type (ia{sv}av) used by com.canonical.dbusmenu.
type menuNode struct {
	ID         int32
	Properties map[string]dbus.Variant
	Children   []dbus.Variant
}

type eventEntry struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

type groupPropEntry struct {
	ID    int32
	Props map[string]dbus.Variant
}

// dbusMenu implements com.canonical.dbusmenu with a static 3-item menu.
type dbusMenu struct {
	revision uint32
	onShow   func()
	onQuit   func()
}

func itemNode(id int32, label string) dbus.Variant {
	return dbus.MakeVariant(menuNode{
		ID: id,
		Properties: map[string]dbus.Variant{
			"label":   dbus.MakeVariant(label),
			"enabled": dbus.MakeVariant(true),
			"visible": dbus.MakeVariant(true),
		},
		Children: []dbus.Variant{},
	})
}

func sepNode(id int32) dbus.Variant {
	return dbus.MakeVariant(menuNode{
		ID: id,
		Properties: map[string]dbus.Variant{
			"type":    dbus.MakeVariant("separator"),
			"visible": dbus.MakeVariant(true),
		},
		Children: []dbus.Variant{},
	})
}

func (m *dbusMenu) GetLayout(parentID, recursionDepth int32, propertyNames []string) (uint32, menuNode, *dbus.Error) {
	root := menuNode{
		ID:         0,
		Properties: map[string]dbus.Variant{},
		Children: []dbus.Variant{
			itemNode(1, "Show Lokihub"),
			sepNode(2),
			itemNode(3, "Quit"),
		},
	}
	return m.revision, root, nil
}

func (m *dbusMenu) GetGroupProperties(ids []int32, propertyNames []string) ([]groupPropEntry, *dbus.Error) {
	return []groupPropEntry{}, nil
}

func (m *dbusMenu) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(""), nil
}

func (m *dbusMenu) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID == "clicked" {
		switch id {
		case 1:
			go m.onShow()
		case 3:
			go m.onQuit()
		}
	}
	return nil
}

func (m *dbusMenu) EventGroup(events []eventEntry) ([]int32, *dbus.Error) {
	for _, e := range events {
		m.Event(e.ID, e.EventID, e.Data, e.Timestamp)
	}
	return []int32{}, nil
}

func (m *dbusMenu) AboutToShow(id int32) (bool, *dbus.Error) {
	return false, nil
}

func (m *dbusMenu) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

func pngToPixmaps(data []byte) []pixmap {
	if len(data) == 0 {
		return []pixmap{}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return []pixmap{}
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	argb := make([]byte, w*h*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bv, a := img.At(x, y).RGBA()
			i := ((y-b.Min.Y)*w + (x - b.Min.X)) * 4
			argb[i] = byte(a >> 8)    //nolint:gosec // deliberate byte extraction from a 16-bit RGBA channel, not a narrowing overflow
			argb[i+1] = byte(r >> 8)  //nolint:gosec // deliberate byte extraction from a 16-bit RGBA channel, not a narrowing overflow
			argb[i+2] = byte(g >> 8)  //nolint:gosec // deliberate byte extraction from a 16-bit RGBA channel, not a narrowing overflow
			argb[i+3] = byte(bv >> 8) //nolint:gosec // deliberate byte extraction from a 16-bit RGBA channel, not a narrowing overflow
		}
	}
	return []pixmap{{int32(w), int32(h), argb}} //nolint:gosec // tray icon dimensions are always tiny
}

func HideFromDock() {}
func ShowInDock()   {}

func Setup(title string, icon []byte, onShow func(), onQuit func()) {
	go func() {
		conn, err := dbus.SessionBus()
		if err != nil {
			return
		}

		svcName := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
		reply, err := conn.RequestName(svcName, dbus.NameFlagDoNotQueue)
		if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
			return
		}

		pixmaps := pngToPixmaps(icon)
		tt := tooltip{Title: title, Pixmaps: []pixmap{}}

		propsSpec := prop.Map{
			sniIface: {
				"Category":   &prop.Prop{Value: "ApplicationStatus", Writable: false, Emit: prop.EmitFalse},
				"Id":         &prop.Prop{Value: "lokihub", Writable: false, Emit: prop.EmitFalse},
				"Title":      &prop.Prop{Value: title, Writable: false, Emit: prop.EmitFalse},
				"Status":     &prop.Prop{Value: "Active", Writable: false, Emit: prop.EmitFalse},
				"IconName":   &prop.Prop{Value: "lokihub", Writable: false, Emit: prop.EmitFalse},
				"IconPixmap": &prop.Prop{Value: pixmaps, Writable: false, Emit: prop.EmitFalse},
				"Menu":       &prop.Prop{Value: menuPath, Writable: false, Emit: prop.EmitFalse},
				"ToolTip":    &prop.Prop{Value: tt, Writable: false, Emit: prop.EmitFalse},
				"ItemIsMenu": &prop.Prop{Value: false, Writable: false, Emit: prop.EmitFalse},
			},
		}
		if _, err = prop.Export(conn, sniPath, propsSpec); err != nil {
			return
		}

		sni := &sniMethods{onShow: onShow, onQuit: onQuit}
		conn.Export(sni, sniPath, sniIface)

		menu := &dbusMenu{revision: 1, onShow: onShow, onQuit: onQuit}
		conn.Export(menu, menuPath, menuIface)

		watcher := conn.Object(watcherSvc, watcherPath)
		watcher.Call(watcherSvc+".RegisterStatusNotifierItem", 0, svcName)

		select {}
	}()
}
