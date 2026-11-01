package lspservice

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nip46/bunker"
)

// errBothOrNeither is returned when the endpoint is configured but the
// operator supplied either no admin key or both forms at once. Exactly one of
// nsec/bunker must be present (plan §3.2).
var errBothOrNeither = errors.New("lspservice: set exactly one of LSP_SERVICE_NSEC or LSP_SERVICE_BUNKER")

// resolveSigner dispatches on which credential is set, mirroring ncli's
// keyresolve.ResolveSigner prefix dispatch (plan §3.3) against nmilat directly:
//
//   - a bunker:// URI  -> a remote NIP-46 signer (bunker.Client)
//   - a raw nsec / hex -> an in-memory key (nip46.LocalKey)
//
// Both satisfy nip46.Signer, so the caller never cares where the key lives.
//
// SECURITY (audit NCLI-0001): this is the CLIENT/dispatch side only. It resolves
// a signer that signs the Hub's OWN outbound requests; it never reuses or drives
// any signer-side Handler.pair / parsePerms permission logic. The returned
// signature and pubkey are re-verified by the caller before use (see signNIP98).
func resolveSigner(ctx context.Context, nsec, bunkerURI string) (nip46.Signer, func(), error) {
	hasNsec := nsec != ""
	hasBunker := bunkerURI != ""
	if hasNsec == hasBunker {
		return nil, nil, errBothOrNeither
	}

	if hasBunker {
		// bunker.Dial's ctx bounds the pairing/connect handshake only; the
		// returned Client lives until Close. Metadata is a display hint shown
		// to the remote signer, never an authorization input.
		c, err := bunker.Dial(ctx, bunkerURI, bunker.ClientOptions{
			Metadata: &nip46.Metadata{Name: "lokihub"},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("lspservice: bunker dial: %w", err)
		}
		return c, func() { _ = c.Close() }, nil
	}

	k, err := nip46.NewLocalKey(nsec) // accepts nsec or 64-hex
	if err != nil {
		return nil, nil, fmt.Errorf("lspservice: invalid LSP_SERVICE_NSEC: %w", err)
	}
	return k, func() {}, nil
}

// getSigner resolves the signer once and caches it. A bunker signer dials on
// first use; a failed resolution is not cached, so the next call retries (a
// signer that was unreachable at boot can attach later).
func (c *Client) getSigner(ctx context.Context) (nip46.Signer, error) {
	c.signerMu.Lock()
	defer c.signerMu.Unlock()
	if c.signer != nil {
		return c.signer, nil
	}
	s, closeFn, err := resolveSigner(ctx, c.cfg.Nsec, c.cfg.Bunker)
	if err != nil {
		return nil, err
	}
	c.signer = s
	c.signerClose = closeFn
	return s, nil
}

// signerGuard is the mutex + cached signer state embedded in Client. Kept in
// this file next to the resolver it guards.
type signerGuard struct {
	signerMu    sync.Mutex
	signer      nip46.Signer
	signerClose func()
}
