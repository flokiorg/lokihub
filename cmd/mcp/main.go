// Command mcp bridges a stdio-only AI assistant to a Lokihub MCP endpoint.
//
//	LOKIHUB_MCP_TOKEN=lkm_... mcp --stdio --url https://hub.example/mcp
//
// The token is read from the environment, never from argv (argv is visible to
// other local users via ps). The bridge opens no wallet and holds no hub secret.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/flokiorg/lokihub/mcp"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:5607/mcp", "the hub's MCP endpoint")
	stdio := flag.Bool("stdio", false, "bridge stdin/stdout (required)")
	insecure := flag.Bool("insecure", false, "skip TLS verification (self-signed dev/regtest hubs only)")
	flag.Parse()

	if !*stdio {
		fmt.Fprintln(os.Stderr, "usage: LOKIHUB_MCP_TOKEN=... mcp --stdio [--url URL]")
		os.Exit(2)
	}
	token := os.Getenv("LOKIHUB_MCP_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "mcp: LOKIHUB_MCP_TOKEN is not set")
		os.Exit(2)
	}

	u, err := neturl.Parse(*url)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		fmt.Fprintln(os.Stderr, "mcp: --url must be https:// (plain http is allowed only for 127.0.0.1/localhost) — the token would otherwise travel in cleartext")
		os.Exit(2)
	}
	client := &http.Client{}
	if *insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // explicit dev-only opt-in
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := &mcp.StdioProxy{Endpoint: *url, Token: token, Client: client}
	if err := p.Run(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		os.Exit(1)
	}
}

func isLoopback(h string) bool { return h == "127.0.0.1" || h == "localhost" || h == "::1" }
