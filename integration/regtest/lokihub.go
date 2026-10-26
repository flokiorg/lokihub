package regtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Lokihub is lokihub's HTTP API on the stack.
type Lokihub struct {
	BaseURL string
	HTTP    *http.Client
}

// NewLokihub returns a client for baseURL.
func NewLokihub(baseURL string) *Lokihub {
	return &Lokihub{BaseURL: baseURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Info is the part of GET /api/info the stack reads.
type Info struct {
	SetupCompleted bool   `json:"setupCompleted"`
	Running        bool   `json:"running"`
	Network        string `json:"network"`
}

// do sends one API call. /api/start and /api/unlock share a limiter of one call
// per second, so a 429 is retried after a short wait.
func (l *Lokihub) do(ctx context.Context, method, path, token string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	var (
		status int
		data   []byte
	)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, l.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := l.HTTP.Do(req)
		if err != nil {
			return err
		}
		data, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		status = resp.StatusCode
		if status != http.StatusTooManyRequests || attempt == 5 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if status >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, status, bytes.TrimSpace(data))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Info reads GET /api/info.
func (l *Lokihub) Info(ctx context.Context) (Info, error) {
	var info Info
	err := l.do(ctx, http.MethodGet, "/api/info", "", nil, &info)
	return info, err
}

// Setup completes first-run setup against the flnd the backend's FLND_* settings name.
func (l *Lokihub) Setup(ctx context.Context, password string) error {
	return l.do(ctx, http.MethodPost, "/api/setup", "", map[string]any{
		"unlockPassword": password,
		"backendType":    "FLND",
		"autoConnect":    true,
	}, nil)
}

// Start starts the node with the unlock password.
func (l *Lokihub) Start(ctx context.Context, password string) error {
	return l.do(ctx, http.MethodPost, "/api/start", "", map[string]any{"unlockPassword": password}, nil)
}

// Unlock mints a full-access admin JWT valid for days.
func (l *Lokihub) Unlock(ctx context.Context, password string, days uint64) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := l.do(ctx, http.MethodPost, "/api/unlock", "", map[string]any{
		"unlockPassword":  password,
		"permission":      "full",
		"tokenExpiryDays": days,
	}, &out)
	return out.Token, err
}

// SetGeneralRelay sets the GeneralRelay setting, which follow-set lookups read.
func (l *Lokihub) SetGeneralRelay(ctx context.Context, token, relay string) error {
	return l.do(ctx, http.MethodPatch, "/api/settings", token, map[string]any{"generalRelay": relay}, nil)
}
