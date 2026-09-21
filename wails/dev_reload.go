//go:build dev
// +build dev

package wails

import (
	"context"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// scheduleDevReload reloads the window once, a few seconds after startup, to
// recover from a `wails dev` startup race: the window opens and requests this
// app's whole module graph the instant Vite reports ready, but Vite is
// single-threaded and can still drop/reset a handful of those under the
// initial burst (see vite.config.ts's `warmup` option, which narrows this but
// doesn't fully close it) — enough to blank the page, since one failed ES
// module import is fatal to the rest. By the time this fires, Vite has had
// several extra seconds on top of the Go app's own build+launch time to fully
// settle, so the reload's own request burst reliably succeeds.
//
// This code ONLY compiles when the 'dev' build tag is present — production
// has no such race (assets are embedded, not proxied over the network).
func scheduleDevReload(ctx context.Context) {
	go func() {
		time.Sleep(5 * time.Second)
		runtime.WindowReload(ctx)
	}()
}
