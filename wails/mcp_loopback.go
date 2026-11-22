package wails

import (
	"context"
	"errors"
	nethttp "net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/http"
	"github.com/flokiorg/lokihub/logger"
)

// MCPLoopbackPortEnv is a legacy override for the local MCP listener port; the
// owner's Settings switch (config MCPLoopbackPort) is the normal way to turn it
// on. Off by default: desktop otherwise has no production network socket.
const MCPLoopbackPortEnv = "LOKIHUB_MCP_LOOPBACK_PORT"

// mcpLoopback opens/closes the 127.0.0.1 MCP listener on the owner's request.
// Every request still needs a valid connection token; the SDK's localhost
// protection rejects cross-origin / DNS-rebinding requests from browsers.
type mcpLoopback struct {
	app  *WailsApp
	mu   sync.Mutex
	hs   *nethttp.Server
	port int
}

// Apply implements http.MCPLoopbackController.
func (l *mcpLoopback) Apply(port int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if port == l.port {
		return nil
	}
	if port == 0 {
		l.stopLocked()
		return nil
	}
	srv := l.app.httpSvc.MCPServer()
	if srv == nil {
		return errors.New("assistant endpoint is not ready")
	}
	hs, addr, err := srv.ListenLoopback(port)
	if err != nil {
		return err // previous listener (if any) is left running
	}
	l.stopLocked()
	l.hs, l.port = hs, port
	logger.Logger.Info().Str("addr", addr.String()).Msg("mcp loopback listener started (owner setting)")
	return nil
}

// Port implements http.MCPLoopbackController.
func (l *mcpLoopback) Port() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.port
}

func (l *mcpLoopback) stopLocked() {
	if l.hs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = l.hs.Shutdown(ctx)
	l.hs, l.port = nil, 0
}

// startMCPLoopback registers the controller with the HTTP layer (so Settings
// can switch it) and applies the saved setting. It returns a stop func.
func (app *WailsApp) startMCPLoopback() func() {
	l := &mcpLoopback{app: app}
	app.httpSvc.SetMCPLoopbackController(l)
	saved, _ := app.svc.GetConfig().Get(http.CfgKeyMCPLoopbackPort, "")
	port, _ := strconv.Atoi(saved)
	if env, _ := strconv.Atoi(os.Getenv(MCPLoopbackPortEnv)); env > 0 {
		port = env
	}
	if port > 0 && port <= 65535 {
		if err := l.Apply(port); err != nil {
			logger.Logger.Error().Err(err).Int("port", port).Msg("mcp loopback: listen failed")
		}
	}
	return func() { _ = l.Apply(0) }
}
