package mcp

import (
	"context"
	"encoding/json"
	"golang.org/x/time/rate"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
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
	activityCap       = 2000 // newest entries returned at most / ring size per kind of call
	activityPerApp    = 500  // per assistant: its writes/denials/approvals
	activityReadsKeep = 100  // per assistant: plain successful reads (noise, kept apart)
	activityDetailMax = 160
)

// ActivityLog is a bounded, newest-first record of assistant tool calls. Each
// assistant has its OWN space, and plain successful reads are kept apart from
// everything else, so no assistant — and no flood of harmless reads — can push
// another call out of the owner's view (pentest F F-2). Every entry is also
// written to the hub log, so the history outlives a restart.
type ActivityLog struct {
	mu    sync.Mutex
	apps  map[uint]*appActivity
	order uint64
	store ActivityStore // optional durable copy (survives restarts)
}

type appActivity struct {
	main  ring
	reads ring
	fails ring          // refused/failed calls: kept apart so they can never evict real activity
	failW *rate.Limiter // throttles durable writes of failures
}

// activityFailsKeep bounds an assistant's in-memory failed calls.
const activityFailsKeep = 100

type stamped struct {
	seq uint64
	e   ActivityEntry
}

type ring struct {
	buf  []stamped
	next int
	full bool
}

func (r *ring) add(x stamped, size int) {
	if r.buf == nil {
		r.buf = make([]stamped, size)
	}
	r.buf[r.next] = x
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

func (r *ring) all() []stamped {
	if r.full {
		return append(append([]stamped(nil), r.buf[r.next:]...), r.buf[:r.next]...)
	}
	return append([]stamped(nil), r.buf[:r.next]...)
}

// NewActivityLog builds an empty log.
func NewActivityLog() *ActivityLog { return &ActivityLog{apps: map[uint]*appActivity{}} }

// Add records an entry. read marks a plain read-tool call.
func (l *ActivityLog) Add(e ActivityEntry) { l.add(e, false) }

func (l *ActivityLog) add(e ActivityEntry, read bool) {
	l.mu.Lock()
	l.order++
	a := l.apps[e.AppID]
	if a == nil {
		a = &appActivity{}
		l.apps[e.AppID] = a
	}
	persist := true
	switch {
	case read && e.Outcome == "done":
		a.reads.add(stamped{l.order, e}, activityReadsKeep)
		persist = false // plain reads stay in memory only (pentest F F-2)
	case e.Outcome == "failed":
		// Failures (unknown tools, missing keys, rate-limited, refused…) can
		// be produced at will, so they get their own space and a throttled
		// durable copy: they never push real activity out (pentest R2 M3).
		a.fails.add(stamped{l.order, e}, activityFailsKeep)
		if a.failW == nil {
			a.failW = rate.NewLimiter(1, 20)
		}
		persist = a.failW.Allow()
	default:
		a.main.add(stamped{l.order, e}, activityPerApp)
	}
	store := l.store
	l.mu.Unlock()
	if store != nil && persist {
		_ = store.Save(e)
	}
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
	if l.store != nil {
		if out, err := l.store.Recent(appID, limit); err == nil {
			l.mu.Lock()
			for id, a := range l.apps {
				if appID == 0 || id == appID {
					for _, x := range append(a.reads.all(), a.fails.all()...) {
						out = append(out, x.e)
					}
				}
			}
			l.mu.Unlock()
			sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
			if len(out) > limit {
				out = out[:limit]
			}
			return out
		}
	}
	l.mu.Lock()
	var all []stamped
	for id, a := range l.apps {
		if appID != 0 && id != appID {
			continue
		}
		all = append(all, a.main.all()...)
		all = append(all, a.reads.all()...)
		all = append(all, a.fails.all()...)
	}
	l.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].seq > all[j].seq })
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]ActivityEntry, len(all))
	for i, x := range all {
		out[i] = x.e
	}
	return out
}

// Forget drops a deleted assistant's in-memory activity (the hub log keeps it).
func (l *ActivityLog) Forget(appID uint) {
	l.mu.Lock()
	delete(l.apps, appID)
	l.mu.Unlock()
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
			// The name is caller-chosen: log only real tool names, so an
			// assistant cannot write arbitrary text into the owner's log.
			outcome, detail := describeCall(res, err)
			if !isSafeToolName(tool) {
				tool, detail = "unknown_tool", "" // the error would echo the forged name
			}
			s.activity.add(ActivityEntry{Time: time.Now().UTC(), AppID: conn.AppID, Tool: clip(tool, 64), Outcome: outcome, Detail: detail}, s.isReadTool(tool))
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
			Recipient  string `json:"recipient"`
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
			if sc.Preview.Recipient != "" {
				d += " → " + sc.Preview.Recipient
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
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return ' '
		case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), r == 0x2028, r == 0x2029:
			return -1 // invisible / direction-changing: dropped
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// isSafeToolName accepts the shape of every registered tool name.
func isSafeToolName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
