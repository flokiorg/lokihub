package interceptor

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"strconv"

	"github.com/rs/zerolog/log"
)

type discordWebhook struct {
	Content string         `json:"content"`
	Embeds  []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title  string              `json:"title"`
	Fields []discordEmbedField `json:"fields"`
	Color  int                 `json:"color"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

func sendDiscordNotification(webhookUrl string, payload discordWebhook) error {
	var buf bytes.Buffer
	err := json.NewEncoder(&buf).Encode(payload)
	if err != nil {
		return fmt.Errorf("failed to encode discord payload: %w", err)
	}

	resp, err := http.Post(webhookUrl, "application/json", &buf)
	if err != nil {
		return fmt.Errorf("failed to send discord notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("received non-success status code from discord: %d", resp.StatusCode)
	}

	return nil
}

func sendOpenChannelDiscordNotification(
	paymentHash []byte, incomingAmountMloki int64,
	destination []byte, capacity int64,
	channelPoint string,
	tag *string,
) {
	notificationsEnabledStr := os.Getenv("DISCORD_NOTIFICATIONS_ENABLED")
	if notificationsEnabledStr != "true" {
		return
	}

	webhookUrl := os.Getenv("DISCORD_WEBHOOK_URL")
	if webhookUrl == "" {
		log.Info().Msg("DISCORD_WEBHOOK_URL is not set, skipping notification")
		return
	}

	tagStr := "N/A"
	if tag != nil {
		tagStr = *tag
	}

	payload := discordWebhook{
		Embeds: []discordEmbed{
			{
				Title: "🚀 Open Channel - Interceptor",
				Color: 5814783, // #58b9ff (Light Blue)
				Fields: []discordEmbedField{
					{Name: "Payment Hash", Value: hex.EncodeToString(paymentHash), Inline: false},
					{Name: "Incoming Amount (mloki)", Value: strconv.FormatUint(uint64(incomingAmountMloki), 10), Inline: true},
					{Name: "Capacity (loki)", Value: strconv.FormatUint(uint64(capacity), 10), Inline: true},
					{Name: "Destination Node", Value: hex.EncodeToString(destination), Inline: false},
					{Name: "Channel Point", Value: channelPoint, Inline: false},
					{Name: "Tag", Value: tagStr, Inline: true},
				},
			},
		},
	}

	err := sendDiscordNotification(webhookUrl, payload)
	if err != nil {
		log.Error().Msgf("Error sending open channel discord notification: %v", err)
	} else {
		log.Info().Msg("Sent open channel discord notification")
	}
}
