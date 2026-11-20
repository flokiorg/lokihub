package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flokiorg/lokihub/logger"
)

// ActivityEntry records one tool call made by an assistant. It carries no tool
// arguments and no results — only what the owner needs to see what happened —
// so it can never leak a payment request, key or token.
type ActivityEntry struct {
	Time    time.Time `json:"time"`
	AppID   uint      `json:"appId"`
	Tool    string    `json:"tool"`
	Outcome string    `json:"outcome"` // done | failed | needs_confirmation | pending_approval | executing | denied | expired
	Detail  string    `json:"detail,omitempty"`
}

const (
	activityCap       = 2000
	activityDetailMax = 160
)

// ActivityLog is a bounded, newest-first record of assistant tool calls. Every
// entry is also written to the hub log, so the history outlives a restart.
type ActivityLog struct {
	mu   sync.Mutex
	buf  []ActivityEntry // ring
	next int
	full bool
}

// NewActivityLog builds an empty log.
func NewActivityLog() *ActivityLog { return &ActivityLog{buf: make([]ActivityEntry, activityCap)} }

// Add records an entry.
func (l *ActivityLog) Add(e ActivityEntry) {
	l.mu.Lock()
	l.buf[l.next] = e
	l.next = (l.next + 1) % len(l.buf)
	if l.next == 0 {
		l.full = true
	}
	l.mu.Unlock()
	logger.Logger.Info().
		Uint("app_id", e.AppID).
		Str("tool", e.Tool).
		Str("outcome", e.Outcome).
		Str("detail", e.Detail).
		Msg("mcp assistant activity")
}

// List returns up to limit entries, newest first; appID 0 = every assistant.
func (l *ActivityLog) List(appID uint, limit int) []ActivityEntry {
	if limit <= 0 || limit > activityCap {
		limit = 100
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.next
	if l.full {
		n = len(l.buf)
	}
	out := make([]ActivityEntry, 0, min(limit, n))
	for i := 1; i <= n && len(out) < limit; i++ {
		e := l.buf[(l.next-i+len(l.buf))%len(l.buf)]
		if appID == 0 || e.AppID == appID {
			out = append(out, e)
		}
	}
	return out
}

// Activity exposes the log to the owner API.
func (s *Server) Activity() *ActivityLog { return s.activity }

// activityMiddleware records every tools/call on a connection's server.
func (s *Server) activityMiddleware(conn *Conn) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			res, err := next(ctx, method, req)
			if method != "tools/call" {
				return res, err
			}
			tool := ""
			if p, ok := req.GetParams().(*mcpsdk.CallToolParamsRaw); ok && p != nil {
				tool = p.Name
			}
			outcome, detail := describeCall(res, err)
			s.activity.Add(ActivityEntry{Time: time.Now().UTC(), AppID: conn.AppID, Tool: clip(tool, 64), Outcome: outcome, Detail: detail})
			return res, err
		}
	}
}

// describeCall turns a tool result into an outcome and a short, non-sensitive
// detail (an error message, an approval summary, or a preview headline).
func describeCall(res mcpsdk.Result, err error) (string, string) {
	if err != nil {
		return "failed", clip(err.Error(), activityDetailMax)
	}
	r, ok := res.(*mcpsdk.CallToolResult)
	if !ok || r == nil {
		return "done", ""
	}
	if r.IsError {
		msg := ""
		for _, c := range r.Content {
			if t, ok := c.(*mcpsdk.TextContent); ok {
				msg = t.Text
				break
			}
		}
		return "failed", clip(msg, activityDetailMax)
	}
	var sc struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
		Preview *struct {
			Action     string `json:"action"`
			AmountLoki string `json:"amount_loki"`
		} `json:"preview"`
	}
	if b, mErr := json.Marshal(r.StructuredContent); mErr == nil {
		_ = json.Unmarshal(b, &sc)
	}
	switch sc.Status {
	case "needs_confirmation":
		d := ""
		if sc.Preview != nil {
			d = sc.Preview.Action
			if sc.Preview.AmountLoki != "" && sc.Preview.AmountLoki != "0" {
				d += " — " + sc.Preview.AmountLoki + " loki"
			}
		}
		return "needs_confirmation", clip(d, activityDetailMax)
	case "pending_approval", "executing", "denied", "expired", "failed":
		return sc.Status, clip(sc.Summary, activityDetailMax)
	case "executed":
		return "done", clip(sc.Summary, activityDetailMax)
	}
	return "done", ""
}

// clip bounds a string for the log (rune-safe) and strips control characters,
// so attacker-influenced text cannot forge extra log lines.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
