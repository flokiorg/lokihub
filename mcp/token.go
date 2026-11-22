package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// HMACTokenStore is a stateless static-bearer TokenStore: the token carries the
// App id and an HMAC over it, keyed by a per-hub secret, so no token table or
// migration is needed in M1. Validity (existence, kind, expiry, revocation) is
// decided by loading the App (AppLoader) — deleting the App revokes the token.
// OAuth replaces this path in M5.
//
// The secret is fetched through a provider on every use, not captured, so the
// store works before the hub is unlocked (provider errors → token invalid) and
// survives a secret rotation (e.g. an unlock-password change invalidates every
// previously minted token at once).
type HMACTokenStore struct {
	secret func() ([]byte, error)
	// gen returns a connection's key generation (bumped by "rotate key"); nil
	// = always 0. Generation 0 keeps the original derivation, so keys issued
	// before rotation existed stay valid until rotated.
	gen func(appID uint) (uint64, error)
}

// ErrNoSecret is returned when the hub secret is unavailable (e.g. locked).
var ErrNoSecret = fmt.Errorf("mcp: hub secret unavailable")

// NewHMACTokenStore builds a token store keyed by a per-hub secret provider.
func NewHMACTokenStore(secret func() ([]byte, error)) *HMACTokenStore {
	return &HMACTokenStore{secret: secret}
}

// WithGeneration makes keys depend on each connection's key generation, so
// rotating a key kills the old one (critic G4).
func (s *HMACTokenStore) WithGeneration(gen func(appID uint) (uint64, error)) *HMACTokenStore {
	s.gen = gen
	return s
}

const tokenPrefix = "lkm_" // lokihub mcp token

// Mint issues a bearer token for an App id. It errors if the secret is
// unavailable.
func (s *HMACTokenStore) Mint(appID uint) (string, error) {
	mac, err := s.mac(appID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d_%s", tokenPrefix, appID, mac), nil
}

func (s *HMACTokenStore) mac(appID uint) (string, error) {
	sec, err := s.secret()
	if err != nil {
		return "", err
	}
	if len(sec) == 0 {
		return "", ErrNoSecret
	}
	var g uint64
	if s.gen != nil {
		if g, err = s.gen(appID); err != nil {
			return "", err
		}
	}
	m := hmac.New(sha256.New, sec)
	// Domain-separated so the secret can't be reused to forge other artifacts.
	if g == 0 {
		_, _ = fmt.Fprintf(m, "mcp-token-v1:%d", appID)
	} else {
		_, _ = fmt.Fprintf(m, "mcp-token-v1:%d:%d", appID, g)
	}
	return hex.EncodeToString(m.Sum(nil)), nil
}

// AppID validates a presented token and returns the App id it authenticates.
// The HMAC is compared in constant time; a secret error fails closed.
func (s *HMACTokenStore) AppID(_ context.Context, token string) (uint, bool) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return 0, false
	}
	rest := token[len(tokenPrefix):]
	sep := strings.IndexByte(rest, '_')
	if sep <= 0 || sep == len(rest)-1 {
		return 0, false
	}
	idStr, mac := rest[:sep], rest[sep+1:]
	id64, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || strconv.FormatUint(id64, 10) != idStr {
		return 0, false // one spelling per key (no leading zeros / signs)
	}
	appID := uint(id64)
	want, err := s.mac(appID)
	if err != nil {
		return 0, false
	}
	if !hmac.Equal([]byte(mac), []byte(want)) {
		return 0, false
	}
	return appID, true
}

// Available reports whether the hub secret can be read (false while the hub
// is locked, before setup, or after a password change cleared it).
func (s *HMACTokenStore) Available() bool {
	sec, err := s.secret()
	return err == nil && len(sec) > 0
}

// ErrHubLocked — the hub cannot verify any token right now (it is locked).
// Clients get 503, not 401: a 401 tells an OAuth client its token is dead and
// to discard it, which is wrong for a hub that is merely locked (critic O2).
var ErrHubLocked = fmt.Errorf("mcp: the hub is locked; unlock it in the Lokihub app")
