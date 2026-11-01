package lspservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
)

// kindHTTPAuth is NIP-98's HTTP-auth event kind (NIP-98 / lspd auth.KindHttpAuth).
const kindHTTPAuth = 27235

// signNIP98 builds a fresh kind-27235 event binding the request URL, method, a
// SHA-256 of the body and a random nonce, signs it with the resolved signer,
// and returns the "Nostr <base64(event)>" Authorization header value.
//
// Built directly against nmilat/nip01 rather than nmilat/nip98.NewAuthHeader
// for two reasons the plan (§3.3) anticipated:
//
//  1. NewAuthHeader takes a raw private key hex, which the bunker (remote
//     signer) path cannot supply — signing must go through nip46.Signer.
//  2. NewAuthHeader emits a "method" tag, but lspd's verifier
//     (lspd/transport/auth/nostr.go) matches on an "m" tag. We emit "m".
//
// A fresh event (fresh created_at + nonce) is produced per request; nothing is
// reused. lspd's go-nostr CheckSignature accepts the event because it is a
// standard NIP-01 schnorr-signed event regardless of which library produced it.
func (c *Client) signNIP98(ctx context.Context, signer nip46.Signer, url, method string, body []byte) (string, error) {
	sum := sha256.Sum256(body)

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("lspservice: nonce: %w", err)
	}

	ev := &nip01.Event{
		Kind:      kindHTTPAuth,
		CreatedAt: uint64(time.Now().Unix()),
		Tags: [][]string{
			{"u", url},
			{"m", method},
			{"payload", hex.EncodeToString(sum[:])},
			{"nonce", hex.EncodeToString(nonce)},
		},
		Content: "",
	}

	if err := signer.Sign(ctx, ev); err != nil {
		return "", fmt.Errorf("lspservice: sign: %w", err)
	}

	// Defense-in-depth (audit NCLI-0001): a remote signer is not trusted to
	// return what we asked for. Pin the pubkey to the resolved signer's and
	// re-verify the signature locally before the event leaves the process.
	// WithoutPowCheck: our anti-replay "nonce" tag collides with NIP-13's tag
	// name but is not proof-of-work, and lspd does not PoW-check either.
	if ev.PubKey != signer.PubKey() {
		return "", fmt.Errorf("lspservice: signer returned pubkey %q, expected %q", ev.PubKey, signer.PubKey())
	}
	if err := ev.Verify(nip01.WithoutPowCheck()); err != nil {
		return "", fmt.Errorf("lspservice: signer returned an invalid signature: %w", err)
	}

	evJSON, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("lspservice: marshal auth event: %w", err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(evJSON), nil
}
