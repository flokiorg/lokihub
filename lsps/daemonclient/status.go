package daemonclient

import (
	"context"
	"errors"
	"time"
)

// Status is the "LSP attached" surface (plan §3.4). It degrades gracefully:
// a configured-but-unreachable lspd yields Reachable=false with an Error, and
// never an error to the caller.
type Status struct {
	// Configured is always true for a non-nil Client (an endpoint is set).
	Configured bool
	// Reachable is whether lspd's public /info answered.
	Reachable bool
	// Attached is Reachable AND the admin key was accepted (authenticated).
	Attached bool
	// Endpoint is lspd's dashboard base URL.
	Endpoint string
	// Pubkey is the admin pubkey resolved from the nsec/bunker (hex).
	Pubkey string
	// Version/Alias come from lspd /info when reachable.
	Version string
	Alias   string
	// Error explains a non-attached state: "unreachable", "key mismatch (403)",
	// or a signer/transport error. Empty when attached.
	Error string
	// CheckedAt is when this status was last probed.
	CheckedAt time.Time
}

// Status returns the cached attach status, refreshing it when stale. The first
// call probes synchronously (bounded by probeTimeout); later stale calls return
// the last value and refresh in the background (stale-while-revalidate), so the
// frequently-polled /api/info never blocks on lspd.
//
// Note: this on-demand cache stands in for the plan's §3.4 boot+ticker health
// loop; the TTL bounds how often lspd is probed. A dedicated ticker/backoff
// loop can be layered on later without changing this contract.
func (c *Client) Status(context.Context) Status {
	c.statusMu.Lock()
	have := !c.cachedAt.IsZero()
	if have && time.Since(c.cachedAt) < statusTTL {
		s := c.cached
		c.statusMu.Unlock()
		return s
	}
	if have {
		if !c.refreshing {
			c.refreshing = true
			go func() {
				s := c.probe()
				c.statusMu.Lock()
				c.cached = s
				c.cachedAt = time.Now()
				c.refreshing = false
				c.statusMu.Unlock()
			}()
		}
		s := c.cached
		c.statusMu.Unlock()
		return s
	}
	c.statusMu.Unlock()

	// First call: probe synchronously so the UI gets a real status immediately.
	s := c.probe()
	c.statusMu.Lock()
	c.cached = s
	c.cachedAt = time.Now()
	c.statusMu.Unlock()
	return s
}

// probe runs the actual health check against lspd using a fresh bounded context
// (not a request context, so a cancelled HTTP request can't abort a shared
// refresh): resolve the signer for the pubkey, GET /info for reachability +
// version + alias, then an authenticated get_summary ping to confirm the admin
// key is accepted (a 403 is a key mismatch).
func (c *Client) probe() Status {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	s := Status{Configured: true, Endpoint: c.cfg.Endpoint, CheckedAt: time.Now()}

	signer, signerErr := c.getSigner(ctx)
	if signerErr == nil {
		s.Pubkey = signer.PubKey()
	} else {
		s.Error = signerErr.Error()
	}

	info, err := c.Info(ctx)
	if err != nil {
		if s.Error == "" {
			s.Error = "unreachable"
		}
		return s
	}
	s.Reachable = true
	s.Version = info.Version
	if len(info.Nodes) > 0 {
		s.Alias = info.Nodes[0].Name
	}

	// Reachable but no usable signer: report reachable, keep the signer error.
	if signerErr != nil {
		return s
	}

	if err := c.ping(ctx); err != nil {
		if errors.Is(err, ErrForbidden) {
			s.Error = "key mismatch (403)"
		} else {
			s.Error = err.Error()
		}
		return s
	}

	s.Attached = true
	s.Error = ""
	return s
}

// ping confirms the admin key is accepted. get_summary is used because it is
// the plan's §3.4 ping method; any HTTP 200 (even a JSON-RPC error such as "No
// nodes configured") proves the request reached the handler, i.e. the signer
// was authorized. Only HTTP 403 means the pubkey is not the admin key.
func (c *Client) ping(ctx context.Context) error {
	err := c.call(ctx, "get_summary", nil, nil)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrForbidden) {
		return ErrForbidden
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		// Reached the handler -> authenticated; the business error is irrelevant
		// to whether the key is accepted.
		return nil
	}
	return err
}
