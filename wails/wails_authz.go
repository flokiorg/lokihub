package wails

import (
	"regexp"
	"strings"
	"sync"
)

// errFullAccessRequired is the refusal returned to the renderer when a
// full-access route is reached without a full-access desktop session. It
// mirrors the message the HTTP requireFullAccess middleware returns to a
// readonly token (http/http_service.go).
const errFullAccessRequired = "This operation requires full access permissions"

// desktopSession holds the unlock state and permission tier for the desktop
// (Wails) IPC surface. The HTTP API gates fund-moving routes with echojwt +
// requireFullAccess and drops the token on lock; the desktop router gets no
// per-call token, so the equivalent state lives here and is consulted before
// full-access routes run.
type desktopSession struct {
	mu sync.RWMutex
	// explicit is set once the router (or a test) establishes a session via
	// unlock/start/stop. Until then the tier is derived from the node's running
	// state so an auto-unlocked node (started at launch, before any renderer
	// unlock — see service.go) is treated as the full-access session its owner
	// opted into.
	explicit   bool
	unlocked   bool
	permission string // "full" or "readonly"
}

// markUnlocked records a unlocked session at the given permission tier. Called
// from the /api/unlock and /api/start router cases (and from tests).
func (s *desktopSession) markUnlocked(permission string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.explicit = true
	s.unlocked = true
	s.permission = permission
}

// markLocked clears the session so a locked desktop app refuses fund-moving
// ops even if the node has not finished tearing down. Called from the /api/stop
// router case (the desktop lock action).
func (s *desktopSession) markLocked() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.explicit = true
	s.unlocked = false
	s.permission = ""
}

func (s *desktopSession) snapshot() (explicit, unlocked bool, permission string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.explicit, s.unlocked, s.permission
}

// hasFullAccess reports whether full-access (fund-moving) routes may run. It
// mirrors the HTTP requireFullAccess middleware: a full-access route runs only
// for a "full" session. A readonly session or a locked app is refused, exactly
// as the HTTP API refuses a readonly token (403) and as a discarded token
// blocks every full-access route after lock.
func (app *WailsApp) hasFullAccess() bool {
	explicit, unlocked, permission := app.session.snapshot()
	if explicit {
		return unlocked && permission == "full"
	}
	// No explicit router session yet. Auto-unlock (service.go) can start the
	// node at launch before the renderer unlocks; mirror that running node as
	// the full-access session its owner configured. A stopped/never-started
	// node has a nil LN client, so pre-unlock and post-lock both fall through
	// to a refusal here.
	return app.svc != nil && app.svc.GetLNClient() != nil
}

var (
	// Readonly routes with a path parameter. GET-only; mirrors the HTTP
	// readOnlyApiGroup (http/http_service.go). Single-segment only, so the
	// deeper full-access children of /api/apps/:id are not shadowed.
	readOnlyAppRegex         = regexp.MustCompile(`^/api/apps/[^/]+$`)
	readOnlyTransactionRegex = regexp.MustCompile(`^/api/transactions/[^/]+$`)
	readOnlySwapRegex        = regexp.MustCompile(`^/api/swaps/[^/]+$`)
	readOnlyLogRegex         = regexp.MustCompile(`^/api/log/[^/]+$`)
)

// publicDesktopRoutes are reachable with no session, mirroring the ungrouped
// HTTP routes (setup/unlock/start/backup/restore/info/...). They establish or
// precede a session, so gating them would deadlock the unlock flow.
var publicDesktopRoutes = map[string]map[string]bool{
	"GET": {
		"/api/info":         true,
		"/api/setup/config": true,
		"/api/setup/status": true,
		"/api/loki/info":    true,
		"/api/loki/rates":   true,
		"/api/loki/faq":     true,
		"/api/currencies":   true,
	},
	"POST": {
		"/api/setup":                  true,
		"/api/setup/local":            true,
		"/api/setup/manual":           true,
		"/api/restore":                true,
		"/api/start":                  true,
		"/api/unlock":                 true,
		"/api/backup":                 true,
		"/api/lsps5/webhook-callback": true,
	},
}

// readOnlyDesktopRoutes mirror the HTTP readOnlyApiGroup: readable by any
// unlocked tier. The desktop router has never required a session to read, so
// these stay ungated; only full-access routes are gated (the fix is additive).
var readOnlyDesktopRoutes = map[string]map[string]bool{
	"GET": {
		"/api/apps":                 true,
		"/api/channels":             true,
		"/api/node/connection-info": true,
		"/api/node/status":          true,
		"/api/node/network-graph":   true,
		"/api/node/transactions":    true,
		"/api/peers":                true,
		"/api/wallet/address":       true,
		"/api/wallet/capabilities":  true,
		"/api/transactions":         true,
		"/api/balances":             true,
		"/api/mempool":              true,
		"/api/health":               true,
		"/api/commands":             true,
		"/api/swaps":                true,
		"/api/swaps/out/info":       true,
		"/api/swaps/in/info":        true,
		"/api/autoswap":             true,
		"/api/forwards":             true,
		"/api/appstore/apps":        true,
		"/api/lsps2/info":           true,
	},
	"POST": {
		"/api/invoices/estimate-fee": true,
	},
}

// routeRequiresFullAccess classifies a desktop IPC route against the HTTP auth
// model: public and readonly routes are left ungated (false); every other /api
// route is full-access (true), fail-closed so a new full-access route is gated
// by default rather than exposed.
func (app *WailsApp) routeRequiresFullAccess(route, method string) bool {
	path := strings.SplitN(route, "?", 2)[0]

	// Only /api routes are gated. /logout (and any non-/api route) is public.
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	if path == "/api/logout" {
		return false
	}

	// Full-access routes that a broader readonly pattern would otherwise shadow
	// must be caught first. The swap mnemonic is a BIP39 seed controlling swap
	// funds (LOKIHUB-0042): full-access only, never readonly, even though it
	// matches the /api/swaps/:swapId readonly lookup pattern.
	if path == "/api/swaps/mnemonic" {
		return true
	}

	if publicDesktopRoutes[method][path] {
		return false
	}
	if readOnlyDesktopRoutes[method][path] {
		return false
	}

	// Public routes with a path parameter.
	if method == "GET" && strings.HasPrefix(path, "/api/appstore/logos/") {
		return false
	}

	// Readonly routes with a path parameter (GET only).
	if method == "GET" {
		if strings.HasPrefix(path, "/api/circle/avatar-proxy") ||
			readOnlyAppRegex.MatchString(path) ||
			readOnlyTransactionRegex.MatchString(path) ||
			readOnlySwapRegex.MatchString(path) ||
			readOnlyLogRegex.MatchString(path) {
			return false
		}
	}

	return true
}
