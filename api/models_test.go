package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func fullInfoResponse() InfoResponse {
	return InfoResponse{
		BackendType:                 "FLND",
		SetupCompleted:              true,
		Running:                     true,
		Unlocked:                    false,
		Version:                     "0.3.0-alpha",
		Network:                     "main",
		StartupState:                "",
		StartupError:                "",
		AutoUnlockPasswordSupported: true,
		AutoUnlockPasswordEnabled:   true,
		Currency:                    "USD",
		FlokicoinDisplayFormat:      "auto",
		Relays:                      []InfoResponseRelay{{Url: "wss://relay.example.com", Online: true}},
		Relay:                       "wss://relay.example.com",
		GeneralRelay:                "wss://relay.example.com",
		SearchRelay:                 "wss://relay.example.com",
		NodeAlias:                   "my-node",
		MempoolUrl:                  "https://mempool.example.com",
		LSPs:                        []LSPInfo{{Name: "Flowgate", Pubkey: "03abc", Host: "1.2.3.4:5521"}},
		LokihubServicesURL:          "https://example.com/services",
		SwapServiceUrl:              "https://example.com/swap",
		MessageboardNwcUrl:          "https://example.com/board",
		EnableSwap:                  true,
		EnableMessageboardNwc:       true,
		WorkDir:                     "/home/user/.lokihub",
		EnablePolling:               true,
	}
}

// Fields that must survive redaction regardless of auth state, because the
// pre-auth setup/unlock UI depends on them to drive routing and diagnostics.
func assertAlwaysPublicFieldsUnchanged(t *testing.T, before, after InfoResponse) {
	t.Helper()
	assert.Equal(t, before.SetupCompleted, after.SetupCompleted)
	assert.Equal(t, before.Running, after.Running)
	assert.Equal(t, before.StartupState, after.StartupState)
	assert.Equal(t, before.StartupError, after.StartupError)
	assert.Equal(t, before.StartupErrorTime, after.StartupErrorTime)
}

func TestInfoResponse_RedactForUnauthenticated_SetupCompleted(t *testing.T) {
	info := fullInfoResponse()
	before := info

	info.RedactForUnauthenticated()

	assertAlwaysPublicFieldsUnchanged(t, before, info)

	assert.Empty(t, info.BackendType)
	assert.Empty(t, info.Version)
	assert.Empty(t, info.Network)
	assert.False(t, info.AutoUnlockPasswordSupported)
	assert.False(t, info.AutoUnlockPasswordEnabled)
	assert.Empty(t, info.Currency)
	assert.Empty(t, info.FlokicoinDisplayFormat)
	assert.Empty(t, info.Relays)
	assert.Empty(t, info.Relay)
	assert.Empty(t, info.GeneralRelay)
	assert.Empty(t, info.SearchRelay)
	assert.Empty(t, info.NodeAlias)
	assert.Empty(t, info.MempoolUrl)
	assert.Empty(t, info.LSPs)
	assert.Empty(t, info.LokihubServicesURL)
	assert.Empty(t, info.SwapServiceUrl)
	assert.Empty(t, info.MessageboardNwcUrl)
	assert.False(t, info.EnableSwap)
	assert.False(t, info.EnableMessageboardNwc)
	assert.False(t, info.EnablePolling)
}

// During initial setup (SetupCompleted false) there's no credential to
// authenticate with yet, so the setup wizard must keep reading these fields
// unauthenticated. Redaction must be a no-op in that state.
func TestInfoResponse_RedactForUnauthenticated_DuringSetup_NoOp(t *testing.T) {
	info := fullInfoResponse()
	info.SetupCompleted = false
	before := info

	info.RedactForUnauthenticated()

	assert.Equal(t, before, info)
}
