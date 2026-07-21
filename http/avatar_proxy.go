package http

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	avatarProxyTimeout  = 8 * time.Second
	avatarProxyMaxBytes = 3 * 1024 * 1024 // generous for an avatar, not for arbitrary payloads
	avatarProxyCache    = "public, max-age=86400"
)

// avatarProxyClientPinnedTo returns an http.Client whose DialContext is
// forced to connect to pinnedIP regardless of what the target hostname
// re-resolves to at request time. Without this, ensurePublicHost's
// validation and the actual outbound connection are two independent DNS
// lookups — an attacker controlling authoritative DNS for the host (TTL=0)
// could return a public IP for the first (passing validation) and a
// loopback/private/link-local/metadata address for the second (a standard
// DNS-rebinding SSRF bypass). Pinning the already-validated IP for the real
// connection closes that TOCTOU window. TLS SNI/certificate validation still
// uses the original hostname (Transport derives ServerName from the request
// URL, independent of what DialContext actually dials).
func avatarProxyClientPinnedTo(pinnedIP net.IP) *http.Client {
	dialer := &net.Dialer{Timeout: avatarProxyTimeout}
	return &http.Client{
		Timeout: avatarProxyTimeout,
		// Never follow redirects — a redirect could point at a disallowed
		// address after we've already validated the original host, and
		// re-validating each hop isn't worth the complexity for an avatar
		// image. Refusing outright means CheckRedirect makes resp itself the
		// 3xx response, which the status check below then rejects.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(pinnedIP.String(), port))
			},
		},
	}
}

// avatarProxyHandler fetches a remote image server-side and streams it back
// to the frontend, so <img> tags for Nostr profile pictures (an unbounded
// set of hosts, one per kind:0 event) never hotlink the browser directly to
// an arbitrary third party — that would leak the user's IP (and, via
// timing, which pubkeys they look up) to whoever hosts each picture. Only
// this backend talks to those hosts now, which is also why the CSP's
// img-src no longer needs a blanket https: allowance.
func (httpSvc *HttpService) avatarProxyHandler(c echo.Context) error {
	rawURL := c.QueryParam("url")
	if rawURL == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "url is required"})
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "url must be an http(s) URL"})
	}

	pinnedIP, err := ensurePublicHost(c.Request().Context(), parsed.Hostname())
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "url host is not allowed"})
	}

	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, parsed.String(), nil)
	if err != nil {
		return c.JSON(http.StatusBadGateway, ErrorResponse{Message: "failed to build upstream request"})
	}
	req.Header.Set("User-Agent", "lokihub-avatar-proxy/1.0")
	req.Header.Set("Accept", "image/*")

	resp, err := avatarProxyClientPinnedTo(pinnedIP).Do(req)
	if err != nil {
		return c.JSON(http.StatusBadGateway, ErrorResponse{Message: "failed to fetch image"})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.JSON(http.StatusBadGateway, ErrorResponse{Message: "upstream did not return an image"})
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return c.JSON(http.StatusBadGateway, ErrorResponse{Message: "upstream response was not an image"})
	}

	c.Response().Header().Set("Cache-Control", avatarProxyCache)
	c.Response().Header().Set("Content-Type", contentType)
	c.Response().WriteHeader(http.StatusOK)

	_, err = io.Copy(c.Response().Writer, io.LimitReader(resp.Body, avatarProxyMaxBytes))
	return err
}

// ensurePublicHost resolves host and rejects anything that isn't a public
// unicast address — loopback, private (RFC1918/RFC4193), link-local
// (including the 169.254.169.254 cloud metadata address), and unspecified
// addresses are all refused. This handler runs in the user's own local app
// process, so without this check a malicious Nostr profile's "picture" URL
// could make this backend probe the user's own LAN (or a cloud metadata
// endpoint, if ever deployed somewhere reachable) and reflect the response
// back to the browser as an "image".
func ensurePublicHost(ctx context.Context, host string) (net.IP, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("could not resolve host")
	}
	for _, ip := range ips {
		if !isPublicUnicastIP(ip.IP) {
			return nil, errors.New("host resolves to a disallowed address")
		}
	}
	// Return the specific address just validated — the caller pins the
	// actual connection to it (see avatarProxyClientPinnedTo) rather than
	// letting a second, independent DNS lookup decide where traffic goes.
	return ips[0].IP, nil
}

func isPublicUnicastIP(ip net.IP) bool {
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified() &&
		!ip.IsMulticast()
}
