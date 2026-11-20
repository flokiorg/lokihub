package wails

import (
	"context"
	nethttp "net/http"
	"os"
	"strconv"
	"time"

	"github.com/flokiorg/lokihub/logger"
)

// MCPLoopbackPortEnv opts the desktop app into serving the MCP endpoint on
// 127.0.0.1:<port>. Unset or 0 = off (the default): desktop otherwise has no
// production network socket, and this listener is that socket, so it is never
// on unless the owner asks for it.
const MCPLoopbackPortEnv = "LOKIHUB_MCP_LOOPBACK_PORT"

// startMCPLoopback starts the opt-in loopback MCP listener and returns a stop
// func (nil when off or on failure). Bound to 127.0.0.1 only; every request
// still needs a valid connection token; the SDK's localhost protection rejects
// cross-origin / DNS-rebinding requests from browsers.
func (app *WailsApp) startMCPLoopback() func() {
	port, _ := strconv.Atoi(os.Getenv(MCPLoopbackPortEnv))
	if port <= 0 || port > 65535 {
		return nil
	}
	srv := app.httpSvc.MCPServer()
	if srv == nil {
		logger.Logger.Warn().Msg("mcp loopback: MCP server not initialised; listener not started")
		return nil
	}
	hs, addr, err := srv.ListenLoopback(port)
	if err != nil {
		logger.Logger.Error().Err(err).Int("port", port).Msg("mcp loopback: listen failed")
		return nil
	}
	logger.Logger.Info().Str("addr", addr.String()).Msg("mcp loopback listener started (opt-in)")
	return func() { shutdownLoopback(hs) }
}

func shutdownLoopback(hs *nethttp.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
}
