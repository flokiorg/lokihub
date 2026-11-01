package interceptor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendDiscordNotification_PostsPayloadAndSucceeds(t *testing.T) {
	var received discordWebhook
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	payload := discordWebhook{
		Embeds: []discordEmbed{{Title: "test", Color: 123, Fields: []discordEmbedField{{Name: "n", Value: "v"}}}},
	}
	err := sendDiscordNotification(server.URL, payload)
	require.NoError(t, err)
	assert.Equal(t, payload.Embeds[0].Title, received.Embeds[0].Title)
	assert.Equal(t, payload.Embeds[0].Fields[0].Value, received.Embeds[0].Fields[0].Value)
}

func TestSendDiscordNotification_NonSuccessStatusIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := sendDiscordNotification(server.URL, discordWebhook{})
	assert.Error(t, err)
}

func TestSendDiscordNotification_UnreachableUrlIsError(t *testing.T) {
	err := sendDiscordNotification("http://127.0.0.1:0/webhook", discordWebhook{})
	assert.Error(t, err)
}

func TestSendOpenChannelDiscordNotification_SkippedWhenDisabled(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("DISCORD_NOTIFICATIONS_ENABLED", "false")
	t.Setenv("DISCORD_WEBHOOK_URL", server.URL)

	sendOpenChannelDiscordNotification([]byte{1, 2}, 1000, []byte{3, 4}, 100_000, "txid:0", nil)
	assert.Equal(t, 0, calls, "must not call out when notifications are disabled")
}

func TestSendOpenChannelDiscordNotification_SkippedWhenNoWebhookUrl(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("DISCORD_NOTIFICATIONS_ENABLED", "true")
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	sendOpenChannelDiscordNotification([]byte{1, 2}, 1000, []byte{3, 4}, 100_000, "txid:0", nil)
	assert.Equal(t, 0, calls, "must not call out when no webhook URL is configured")
}

func TestSendOpenChannelDiscordNotification_SendsWhenEnabled(t *testing.T) {
	var received discordWebhook
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("DISCORD_NOTIFICATIONS_ENABLED", "true")
	t.Setenv("DISCORD_WEBHOOK_URL", server.URL)

	tag := "promo-tag"
	sendOpenChannelDiscordNotification([]byte{0xde, 0xad}, 5000, []byte{0xbe, 0xef}, 200_000, "abc:1", &tag)

	assert.Equal(t, 1, calls)
	require.Len(t, received.Embeds, 1)
	fieldsByName := map[string]string{}
	for _, f := range received.Embeds[0].Fields {
		fieldsByName[f.Name] = f.Value
	}
	assert.Equal(t, "dead", fieldsByName["Payment Hash"])
	assert.Equal(t, "beef", fieldsByName["Destination Node"])
	assert.Equal(t, "abc:1", fieldsByName["Channel Point"])
	assert.Equal(t, "promo-tag", fieldsByName["Tag"])
}

func TestSendOpenChannelDiscordNotification_DefaultsTagToNA(t *testing.T) {
	var received discordWebhook
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("DISCORD_NOTIFICATIONS_ENABLED", "true")
	t.Setenv("DISCORD_WEBHOOK_URL", server.URL)

	sendOpenChannelDiscordNotification([]byte{1}, 1, []byte{2}, 1, "x:0", nil)

	fieldsByName := map[string]string{}
	for _, f := range received.Embeds[0].Fields {
		fieldsByName[f.Name] = f.Value
	}
	assert.Equal(t, "N/A", fieldsByName["Tag"])
}
