package wails

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/api"
)

type circleStatsAPI struct {
	api.API
	askedFor uint
}

func (a *circleStatsAPI) GetCircleHubStats(appID uint) (*api.CircleHubStatsResponse, error) {
	a.askedFor = appID
	return &api.CircleHubStatsResponse{}, nil
}

// The Circle Hub dashboard loads its stats from this route; the HTTP server
// serves it, so the desktop app must too.
func TestWailsRouter_CircleHubStats(t *testing.T) {
	stub := &circleStatsAPI{}
	app := &WailsApp{api: stub}

	res := app.WailsRequestRouter("/api/apps/7/circle-stats", "GET", "")
	assert.Empty(t, res.Error)
	assert.IsType(t, &api.CircleHubStatsResponse{}, res.Body)
	assert.Equal(t, uint(7), stub.askedFor)
}
