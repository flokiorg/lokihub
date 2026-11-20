package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// StdioProxy bridges a stdio MCP client (newline-delimited JSON-RPC on
// stdin/stdout, as stdio-only clients speak) to a hub's Streamable-HTTP /mcp
// endpoint. It is a pure transport bridge: it opens no wallet, holds no hub
// secret, and authenticates with the connection's bearer token like any remote
// client — so it never conflicts with the hub's one-wallet-per-process model.
type StdioProxy struct {
	Endpoint string // e.g. http://127.0.0.1:5607/mcp or https://hub.example/mcp
	Token    string // the connection's bearer token (never put in the URL)
	Client   *http.Client

	mu        sync.Mutex
	sessionID string
	initMsgs  [][]byte // initialize + notifications/initialized, replayed after a session expiry
}

// errSessionGone is returned internally when the hub no longer knows the session.
var errSessionGone = errors.New("mcp proxy: session expired")

// maxProxyLine bounds one JSON-RPC message read from stdin (DoS guard), matching
// the server's request-body cap.
const maxProxyLine = maxRequestBodyBytes

// Run forwards every stdin line to the endpoint and writes each JSON-RPC message
// in the response (plain JSON or an SSE stream) as one line on out. It returns
// when in is exhausted or ctx is cancelled.
func (p *StdioProxy) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	if p.Client == nil {
		p.Client = http.DefaultClient
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), maxProxyLine)
	var outMu sync.Mutex
	emit := func(msg []byte) error {
		outMu.Lock()
		defer outMu.Unlock()
		if _, err := out.Write(append(bytes.TrimSpace(msg), '\n')); err != nil {
			return err
		}
		return nil
	}
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		msg := append([]byte(nil), line...)
		p.rememberInit(msg)
		err := p.forward(ctx, msg, emit)
		if errors.Is(err, errSessionGone) {
			// The hub reaped an idle session: transparently re-initialize and
			// retry once, instead of killing the assistant's stdio server.
			if err = p.reinit(ctx); err == nil {
				err = p.forward(ctx, msg, emit)
			}
		}
		if err != nil {
			return err
		}
	}
	return sc.Err()
}

// ErrProxyUnauthorized is returned when the hub rejects the token.
var ErrProxyUnauthorized = errors.New("mcp proxy: hub rejected the token (revoked, expired, or wrong hub)")

func (p *StdioProxy) forward(ctx context.Context, msg []byte, emit func([]byte) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	p.mu.Lock()
	if p.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", p.sessionID)
	}
	p.mu.Unlock()

	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("mcp proxy: %w", err)
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		p.mu.Lock()
		p.sessionID = sid
		p.mu.Unlock()
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrProxyUnauthorized
	case resp.StatusCode == http.StatusNotFound && req.Header.Get("Mcp-Session-Id") != "":
		p.mu.Lock()
		p.sessionID = ""
		p.mu.Unlock()
		return errSessionGone
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent:
		return nil // notification / response acknowledged, nothing to relay
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("mcp proxy: hub returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relaySSE(resp.Body, emit)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyLine))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return emit(b)
}

// relaySSE emits the data payload of every SSE event as one JSON-RPC line.
func relaySSE(r io.Reader, emit func([]byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxProxyLine)
	var data bytes.Buffer
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		defer data.Reset()
		return emit(data.Bytes())
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return sc.Err()
}

// rememberInit keeps the client's handshake so it can be replayed.
func (p *StdioProxy) rememberInit(msg []byte) {
	var m struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch m.Method {
	case "initialize":
		p.initMsgs = [][]byte{msg}
	case "notifications/initialized":
		if len(p.initMsgs) == 1 {
			p.initMsgs = append(p.initMsgs, msg)
		}
	}
}

// reinit replays the handshake to obtain a fresh session; its responses are
// swallowed (the client already has them).
func (p *StdioProxy) reinit(ctx context.Context) error {
	p.mu.Lock()
	msgs := append([][]byte(nil), p.initMsgs...)
	p.mu.Unlock()
	if len(msgs) == 0 {
		return errSessionGone
	}
	discard := func([]byte) error { return nil }
	for _, m := range msgs {
		if err := p.forward(ctx, m, discard); err != nil {
			return err
		}
	}
	return nil
}
