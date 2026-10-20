package wails

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The desktop app has no login token to put on an <img> URL, so it fetches
// profile pictures through the router instead, with the same host checks as
// the web route.
func TestWailsRouter_AvatarProxy_RefusesPrivateHost(t *testing.T) {
	app := &WailsApp{}

	res := app.WailsRequestRouter("/api/circle/avatar-proxy?url="+url.QueryEscape("http://127.0.0.1/a.png"), "GET", "")
	assert.Equal(t, "url host is not allowed", res.Error)
	assert.Nil(t, res.Body)
}

func TestWailsRouter_AvatarProxy_RequiresURL(t *testing.T) {
	app := &WailsApp{}

	res := app.WailsRequestRouter("/api/circle/avatar-proxy", "GET", "")
	assert.Equal(t, "url is required", res.Error)
}
