//go:build !dev
// +build !dev

package wails

import "context"

// scheduleDevReload is a no-op outside `wails dev` — see dev_reload.go.
func scheduleDevReload(ctx context.Context) {
}
