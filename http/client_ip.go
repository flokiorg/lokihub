package http

import (
	"net"
	"os"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
)

// TrustedProxiesEnv lists the reverse proxies (CIDRs, comma-separated) whose
// X-Forwarded-For hop the hub believes. Default: loopback only — the bundled
// same-host proxy. A deployment whose proxy reaches the hub over a private
// network (e.g. docker) must list that network explicitly; anything else is
// treated as a direct client and its forwarded headers are ignored
// (GHSA-246p, pentest D PD-7).
const TrustedProxiesEnv = "LOKIHUB_TRUSTED_PROXIES"

var (
	trustedOnce sync.Once
	trustedNets []*net.IPNet
)

func trustedProxies() []*net.IPNet {
	trustedOnce.Do(func() {
		spec := os.Getenv(TrustedProxiesEnv)
		if strings.TrimSpace(spec) == "" {
			spec = "127.0.0.0/8,::1/128"
		}
		for _, c := range strings.Split(spec, ",") {
			if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
				trustedNets = append(trustedNets, n)
			}
		}
	})
	return trustedNets
}

func isTrustedProxy(ip net.IP) bool {
	for _, n := range trustedProxies() {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP is the real client address for rate limiting: the TCP peer, unless
// that peer is a trusted proxy, in which case the LAST X-Forwarded-For hop —
// the one that proxy appended itself, which a client cannot forge.
func clientIP(c echo.Context) string {
	direct := echo.ExtractIPDirect()(c.Request())
	ip := net.ParseIP(direct)
	if ip == nil || !isTrustedProxy(ip) {
		return direct
	}
	// Walk X-Forwarded-For from the right, skipping hops that are themselves
	// trusted proxies; the first untrusted hop is the client. A trusted proxy
	// that merely passes a client's header through (no hop of its own) would
	// otherwise let that client name any address (pentest R3-B #2) — so every
	// trusted proxy must append its peer (Vite: xfwd), and a hop we cannot
	// parse ends the walk at the last trusted address.
	hops := strings.Split(strings.Join(c.Request().Header.Values("X-Forwarded-For"), ","), ",")
	client := direct
	for i := len(hops) - 1; i >= 0; i-- {
		h := net.ParseIP(strings.TrimSpace(hops[i]))
		if h == nil {
			return client
		}
		client = h.String()
		if !isTrustedProxy(h) {
			return client
		}
	}
	return client
}
