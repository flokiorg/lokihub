//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// bearerSecretAndHash returns a fresh random bearer secret (hex) and the
// hex-encoded sha256 commitment of it — the value a caller submits as a
// bearer new_identity's identity_value in cash_transfer (NIP-JW §Bearer
// Slices): the wallet never mints or returns a bearer secret over the
// shared cash_wallet connection, so the caller always generates their own.
func bearerSecretAndHash(t *testing.T) (secretHex, hashHex string) {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(raw), hex.EncodeToString(hash[:])
}

// Mirrors the unexported kind constants in
// nip47/controllers/cash_redeem_controller.go (Kind 23198: NIP-CASH's
// per-claim proof of identity, bound to one wallet + one invoice; Kind
// 35522: Identity Authority attestation, connection_key mode only — this one
// is NIP-IC's own kind, genuinely reused rather than owned by NIP-CASH) and
// nip47/controllers/create_circle_wallet_identity.go (Kind 23199: NIP-CW's
// own, separate per-call identity proof).
const (
	nostrKindClaimProof          = 23198
	nostrKindCircleIdentityProof = 23199
	nostrKindIAAttestation       = 35522
)

// buildClaimProofEvent builds and signs a kind-23198 claim proof bound to
// walletPubkey and bolt11Hash — the binding that makes an intercepted proof
// unusable for any invoice other than the one it was signed for, which
// matters because a cash_wallet's connection is meant to be shared/public.
// extraTags carries the connection_key-mode-only tags (connection_key + an
// e-tag referencing the attestation event).
func buildClaimProofEvent(t *testing.T, signerPrivkey, walletPubkey, bolt11Hash string, extraTags nostr.Tags, createdAt time.Time) *nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"d", walletPubkey}, {"bolt11_hash", bolt11Hash}}
	tags = append(tags, extraTags...)
	ev := &nostr.Event{
		Kind:      nostrKindClaimProof,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags:      tags,
	}
	require.NoError(t, ev.Sign(signerPrivkey))
	return ev
}

// buildTransferProofEvent builds and signs a kind-23198 transfer proof bound
// to walletPubkey, the target (newIdentityType, newIdentityValue,
// newIAPubkey), AND the exact amountMloki this proof authorizes — the
// binding that stops an intercepted proof from being redirected to a
// different new_identity (including a different Identity Authority for a
// connection_key target — a captured proof used to be replayable with a
// swapped, still-trusted IA even with identity_value unchanged, fixed
// 2026-07-30), or replayed for a different amount_millis, than what it was
// actually signed for (NIP-CASH §Transferring and Splitting a Slice). For a
// full transfer, pass the slice's exact current amount (never a
// sentinel/omitted value — the server resolves an omitted request
// amount_millis to the slice's live full amount and requires the proof to
// match that exact number). Mirrors buildClaimProofEvent, but bound via
// new_identity_hash/amount_millis instead of bolt11_hash
// (nip47/controllers/cash_transfer_controller.go's newIdentityHash).
// newIdentityValue is "" for a bearer target; newIAPubkey is "" for
// non-connection_key targets.
func buildTransferProofEvent(t *testing.T, signerPrivkey, walletPubkey, newIdentityType, newIdentityValue, newIAPubkey string, amountMloki uint64, extraTags nostr.Tags, createdAt time.Time) *nostr.Event {
	t.Helper()
	sum := sha256.Sum256([]byte(newIdentityType + ":" + newIdentityValue + ":" + newIAPubkey))
	tags := nostr.Tags{
		{"d", walletPubkey},
		{"new_identity_hash", hex.EncodeToString(sum[:])},
		{"amount_millis", strconv.FormatUint(amountMloki, 10)},
	}
	tags = append(tags, extraTags...)
	ev := &nostr.Event{
		Kind:      nostrKindClaimProof,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags:      tags,
	}
	require.NoError(t, ev.Sign(signerPrivkey))
	return ev
}

// buildIAAttestationEvent builds and signs a kind-35522 IA attestation,
// carrying the platform/evidence tags verifyClaimAttestationEvent requires
// (matching a real nipIC.NewAttestation-produced event's shape) alongside the
// usual d/p/expiration bindings. platform/evidence content is fixed and not
// test-parameterized since no test here needs to vary it.
func buildIAAttestationEvent(t *testing.T, iaPrivkey, connectionKey, claimantNostrPubkey string, expireOffset time.Duration) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      nostrKindIAAttestation,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", connectionKey},
			{"p", claimantNostrPubkey},
			{"platform", "discord"},
			{"evidence", `{"version":1,"platform":"discord","auth_type":"public_post","user_id":"test-user"}`},
			{"expiration", fmt.Sprintf("%d", time.Now().Add(expireOffset).Unix())},
		},
	}
	require.NoError(t, ev.Sign(iaPrivkey))
	return ev
}

// buildCircleWalletIdentityEvent builds and signs a kind-23199 proof that the
// caller controls requesterPrivkey, bound to this specific circle hub via the
// d-tag (nip47/controllers/create_circle_wallet_identity.go) — hubAppPubkey
// is the hub connection's own ClientPubkey(), not its WalletPubkey().
func buildCircleWalletIdentityEvent(t *testing.T, requesterPrivkey, hubAppPubkey string) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      nostrKindCircleIdentityProof,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", hubAppPubkey}},
	}
	require.NoError(t, ev.Sign(requesterPrivkey))
	return ev
}

// buildCircleWalletIdentityEventCustom is like buildCircleWalletIdentityEvent
// but allows overriding the d-tag and created-at timestamp, for exercising
// malformed/adversarial identity proofs (bound to the wrong hub, stale, or
// with a future timestamp) that the plain helper can't express.
func buildCircleWalletIdentityEventCustom(t *testing.T, signerPrivkey, dTagValue string, createdAt time.Time) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      nostrKindCircleIdentityProof,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags:      nostr.Tags{{"d", dTagValue}},
	}
	require.NoError(t, ev.Sign(signerPrivkey))
	return ev
}

// distinctCircleWalletIdentityEvent is like buildCircleWalletIdentityEvent
// but takes an extra disambiguating tag value, guaranteeing a unique event id
// even when built within the same wall-clock second as another proof for the
// same signer+hub. nostr.Now()/nostr.Timestamp has only second precision, so
// two otherwise-identical proofs built back-to-back for the same identity
// (e.g. by adjacent subtests in circle_hub_test.go) can be byte-identical and
// collide on the single-use replay guard — mirrors
// distinctCircleWalletRequest in
// nip47/controllers/create_circle_wallet_membership_test.go, the same fix at
// the unit-test level.
func distinctCircleWalletIdentityEvent(t *testing.T, signerPrivkey, hubAppPubkey, disambiguator string) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      nostrKindCircleIdentityProof,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", hubAppPubkey}, {"disambiguator", disambiguator}},
	}
	require.NoError(t, ev.Sign(signerPrivkey))
	return ev
}

// eventJSONWithTamperedID marshals ev with its `id` field mutated to a
// different, well-formed-looking value — go-nostr's CheckSignature() doesn't
// verify id against the event's own content (only CheckID() does), so this
// simulates a captured proof resubmitted with only its id field changed, to
// prove the id-tamper defense actually rejects it rather than accepting a
// signature that happens to still verify.
func eventJSONWithTamperedID(t *testing.T, ev *nostr.Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &m))
	tamperedID := strings.Repeat("1", 64)
	require.NotEqual(t, ev.ID, tamperedID)
	m["id"] = tamperedID
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}

func eventJSON(t *testing.T, ev *nostr.Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	return string(b)
}

// requireConfig loads the integration config or skips the test entirely when
// it's missing, so this suite is a no-op for anyone who hasn't provisioned
// real hubs yet.
func requireConfig(t *testing.T) *Config {
	t.Helper()
	path := configPathFromEnv()
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Skipf("skipping: could not load integration config (%v) - see integration/README.md to set one up", err)
	}
	return cfg
}

// mustConnect connects an nwcclient.Client and registers it for cleanup.
func mustConnect(t *testing.T, pairingURI string) *nwcclient.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := nwcclient.Connect(ctx, pairingURI)
	require.NoError(t, err, "connect to %s", pairingURI)
	client.Logger = t.Logf
	t.Cleanup(client.Close)
	return client
}

// newTestPrivkey generates a fresh random nostr private key, e.g. to act as
// a one-off Cash wallet beneficiary.
func newTestPrivkey(t *testing.T) string {
	t.Helper()
	return nostr.GeneratePrivateKey()
}

// requireNWCErrorCode asserts err is an *nwcclient.NWCError with the given code.
func requireNWCErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var nwcErr *nwcclient.NWCError
	require.True(t, errors.As(err, &nwcErr), "expected an *nwcclient.NWCError, got %T: %v", err, err)
	t.Logf("got expected nwc error: code=%s message=%q", nwcErr.Code, nwcErr.Message)
	require.Equal(t, code, nwcErr.Code)
}

// ctxT returns a context bound to the test's default timeout budget.
func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), nwcclient.DefaultCallTimeout)
	t.Cleanup(cancel)
	return ctx
}

// skipIfEnvUnset skips the test unless the given env var is set to a
// non-empty, truthy-ish value — used to gate opt-in tests (e.g. rate-limit
// exhaustion) that consume a real hub's shared hourly quota.
func skipIfEnvUnset(t *testing.T, envVar string) {
	t.Helper()
	if os.Getenv(envVar) == "" {
		t.Skipf("skipping: set %s=1 to run this opt-in test (it consumes real quota on the hub)", envVar)
	}
}

// mustPubkey derives the nostr pubkey for a privkey, failing the test on error.
func mustPubkey(t *testing.T, privkey string) string {
	t.Helper()
	pub, err := nostr.GetPublicKey(privkey)
	require.NoError(t, err)
	return pub
}

// newTestConnectionKey generates a fresh random 32-byte hex string usable as
// a connection_key, e.g. to simulate a one-off LIDP-issued identity. Reusing
// nostr.GeneratePrivateKey() here is just a convenient source of random
// 32-byte hex - it has no nostr-keypair meaning in this context.
func newTestConnectionKey(t *testing.T) string {
	t.Helper()
	return nostr.GeneratePrivateKey()
}

// mintInvoiceFromSimpleWallet mints a real invoice from a fresh, throwaway
// simple wallet (see createEphemeralSimpleWallet) - a plain isolated app kept
// independent of any cash_hub/circle_hub, used to fund invoices for full-drain
// payment scenarios. Every call gets its own ephemeral wallet: nothing in
// this suite ever checks a simple wallet's own balance/history afterward, so
// there's no need to share one across calls (see createEphemeralSimpleWallet
// - unlike circle wallets, a plain isolated app has no per-identity cap to
// worry about reusing around).
func mintInvoiceFromSimpleWallet(t *testing.T, cfg *Config, amountMloki uint64, description string) MakeInvoiceResult {
	t.Helper()
	simpleWallet := createEphemeralSimpleWallet(t, cfg)
	client := mustConnect(t, simpleWallet.Connection)

	var invoice MakeInvoiceResult
	require.NoError(t, client.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      amountMloki,
		Description: description,
	}, &invoice))
	require.NotEmpty(t, invoice.Invoice)
	return invoice
}

// payInvoiceFromSimpleWallet is the mirror of mintInvoiceFromSimpleWallet: a
// fresh, throwaway simple wallet pays a real invoice, used by
// circle_wallet_scope_test.go to fund a circle wallet child from an external
// source.
func payInvoiceFromSimpleWallet(t *testing.T, cfg *Config, invoice string) PayInvoiceResult {
	t.Helper()
	simpleWallet := createEphemeralSimpleWallet(t, cfg)
	client := mustConnect(t, simpleWallet.Connection)

	var result PayInvoiceResult
	require.NoError(t, client.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice}, &result))
	require.NotEmpty(t, result.Preimage)
	return result
}
