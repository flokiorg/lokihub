package nip47

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipIC"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// Session D, Security Auditor A — surface 1, the verification budget.
//
// transport.Item.VerificationCost counts structurally, BEFORE any crypto runs, and
// Envelope.check refuses an envelope whose total exceeds MaxVerifyBudget (200 by
// default). Its doc comment states the property plainly: "a caller must not be able to
// make the hub spend a second of secp256k1 time and only then be told the envelope was
// over budget."
//
// ALREADY SETTLED AND NOT RE-DERIVED HERE: a redeem/transfer's TOP-LEVEL
// identity_event/attestation_event are uncounted, 2 per item, which MaxItems=32 bounds
// at ~226 real verifications against a believed 200 (~84ms vs 75ms). That one is a
// constant factor and is not an amplifier.
//
// What is tested here is a DIFFERENT shape: a cost that scales with a number no envelope
// limit bounds — the target bill's own claim count. nip47Service.attestedConnectionKey
// re-verifies the SAME attestation event once per connection_key claim on the bill, and
// nipIC.ParseAttestation calls nip01.Event.Verify() (full schnorr) as its second
// statement, before any of the cheap field comparisons that will reject it. A bill may
// carry up to maxRecipientsPerWallet = 100 claims.

// auditDSecABillWithConnectionKeyClaims builds a bill with n connection_key claims, each
// with its OWN registered IAPubkey — which is how a real multi-platform bill looks, and
// what stops an attacker's attestation matching on the first iteration.
func auditDSecABillWithConnectionKeyClaims(t *testing.T, svc *tests.TestService, hubID uint, n int) (walletPubkey, connPriv string) {
	t.Helper()

	wallet := db.App{
		Name: "auditD-seca-" + tests.RandomHex32()[:8], Kind: db.AppKindCashWallet,
		ParentAppID: &hubID, ParentKind: db.ParentKindCash,
		AppPubkey: tests.RandomHex32(),
	}
	require.NoError(t, svc.DB.Create(&wallet).Error)

	walletKey, err := svc.Keys.GetAppWalletKey(wallet.ID)
	require.NoError(t, err)
	walletPubkey, err = nostr.GetPublicKey(walletKey)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&wallet).Update("wallet_pubkey", walletPubkey).Error)

	connPriv, err = svc.Keys.GetCashPairingKey(wallet.ID)
	require.NoError(t, err)
	connPub, err := nostr.GetPublicKey(connPriv)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&wallet).Update("app_pubkey", connPub).Error)

	claims := make([]db.CashWalletClaim, 0, n)
	for i := 0; i < n; i++ {
		iaPriv := nostr.GeneratePrivateKey()
		iaPub, err := nostr.GetPublicKey(iaPriv)
		require.NoError(t, err)
		claims = append(claims, db.CashWalletClaim{
			IdentityType:  db.CashIdentityConnectionKey,
			IdentityValue: tests.RandomHex32(),
			IAPubkey:      iaPub,
			AmountMloki:   1000,
		})
	}
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, claims))

	for _, scope := range []string{
		constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE,
		constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE,
	} {
		require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: wallet.ID, Scope: scope}).Error)
	}
	return walletPubkey, connPriv
}

// auditDSecAAttestation builds a kind-35522 attestation that is STRUCTURALLY PERFECT and
// CORRECTLY SIGNED — by the attacker's own key, which no claim on the bill names as its
// IA. That is the expensive shape on purpose: nip01.Event.Verify() checks the id hash and
// then the schnorr signature, so a junk signature or a wrong id would be rejected
// cheaply. A valid signature by an irrelevant key pays the full cost and is then rejected
// by a string compare.
func auditDSecAAttestation(t *testing.T, attackerPriv, attackerPub string) (jsonText string, ev *nip01.Event) {
	t.Helper()
	key := tests.NewTestConnectionKey()
	userID, _ := tests.TestConnectionKeyUserID(key)
	built, err := nipIC.NewAttestation(nipIC.AttestationParams{
		PrivateKey:    attackerPriv,
		ConnectionKey: nipIC.ConnectionKey(key),
		UserPubkey:    attackerPub,
		Platform:      tests.TestPlatform,
		Evidence: nipIC.Evidence{
			UserID: userID, Username: "a", VerifiedAt: time.Now().Unix(),
		},
		AuthType:       "oauth",
		ExpirationDays: 90,
	})
	require.NoError(t, err)
	require.NoError(t, built.Verify(), "the attestation must really verify, or the cost is not paid")

	raw, err := json.Marshal(built)
	require.NoError(t, err)
	return string(raw), built
}

// TestAuditD_SecA_VerificationCostIgnoresPerClaimAttestationReverification is the
// structural half: what the budget believes an item costs.
func TestAuditD_SecA_VerificationCostIgnoresPerClaimAttestationReverification(t *testing.T) {
	attackerPriv := nostr.GeneratePrivateKey()
	attackerPub, err := nostr.GetPublicKey(attackerPriv)
	require.NoError(t, err)
	attestation, _ := auditDSecAAttestation(t, attackerPriv, attackerPub)

	params, err := json.Marshal(map[string]any{
		"scope":             "mine",
		"attestation_event": attestation,
	})
	require.NoError(t, err)

	item := transport.Item{
		ID: "a1", Target: strings.Repeat("cc", 32),
		Method: constants.NIP47MethodCashStatus, Params: params,
		Proof:     json.RawMessage(`{"kind":23192}`),
		BillProof: json.RawMessage(`{"kind":23193}`),
	}

	// The budget counts 3: the kind-23193 bill proof, the kind-23192 slice proof and the
	// attestation the params carry (nmilat K-VB-1). The bill's claim count is still
	// invisible to it, because the hub has not looked the bill up yet.
	assert.Equal(t, 3, item.VerificationCost(),
		"the structural count sees the item's two proofs and the attestation it carries")

	// So a full envelope of such items is believed to cost 2*MaxItems, which is a sixth
	// of the announced budget and therefore never refused.
	limits := transport.DefaultLimits()
	assert.Less(t, 2*limits.MaxItems, limits.MaxVerifyBudget,
		"a full batch of these is comfortably inside the announced budget of %d", limits.MaxVerifyBudget)
	t.Logf("believed cost of a full %d-item batch: %d verifications, announced budget %d",
		limits.MaxItems, 2*limits.MaxItems, limits.MaxVerifyBudget)
}

// TestAuditD_SecA_AttestationIsReverifiedOncePerClaim pins the fix for a cost that
// grew with the bill's claim count: the attestation's signature used to be verified
// once per connection_key claim, so a 100-claim bill cost ~100 verifications for ONE
// item against a transport budget that believes an item costs 2.
//
// Counted, not timed. It used to compare wall-clock time for 1 and 100 claims against
// a reference verification, and that failed under a parallel `go test ./...` from
// load alone (9.2 ms measured against a 6.0 ms ceiling). The parse that carries the
// verification is now behind parseClaimAttestation, so the test counts it directly:
// exactly one per item, whatever the claim count.
func TestAuditD_SecA_AttestationIsReverifiedOncePerClaim(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	attackerPriv := nostr.GeneratePrivateKey()
	attackerPub, err := nostr.GetPublicKey(attackerPriv)
	require.NoError(t, err)
	attestation, _ := auditDSecAAttestation(t, attackerPriv, attackerPub)

	params, err := json.Marshal(map[string]any{
		"scope":             "mine",
		"attestation_event": attestation,
	})
	require.NoError(t, err)
	paramsText := string(params)

	hubXOnly := strings.Repeat("ab", 32)

	var parses int
	real := parseClaimAttestation
	t.Cleanup(func() { parseClaimAttestation = real })
	parseClaimAttestation = func(ev *nostr.Event) (*nipIC.Attestation, error) {
		parses++
		return real(ev)
	}

	for _, n := range []int{1, 25, 50, 100} {
		walletPubkey, connPriv := auditDSecABillWithConnectionKeyClaims(t, svc, hub.ID, n)
		nonce := tests.RandomHex32()
		notAfter := time.Now().Add(time.Minute).Unix()
		item := buildItem(t, "a1", walletPubkey, constants.NIP47MethodCashStatus, paramsText,
			attackerPriv, connPriv, hubXOnly, nonce, notAfter)
		binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

		parses = 0
		result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
		require.True(t, served, "n=%d: an item with a valid bill proof is answered, not omitted", n)
		require.NotNil(t, result.Error, "n=%d: the attacker holds no slice, so this must refuse", n)
		require.Equal(t, constants.ERROR_NOT_FOUND, result.Error.Code,
			"n=%d: the refusal is NOT_FOUND — the hub did the work and then declined", n)
		require.Equal(t, 1, parses,
			"n=%d claims: the attestation was parsed and verified %d times for one item, want once", n, parses)
	}
}

// TestAuditD_SecA_ManyItemsOnOneBillAreNotDuplicates closes the only step of the exploit
// that is not obvious: whether an attacker can actually put MaxItems items naming the
// SAME bill and the SAME method into one envelope, given Envelope.check's
// duplicate-item rule.
//
// It can, because the rule keys on the params HASH, and the attestation_event is part of
// params — so a different (still perfectly valid) attestation in each item makes every
// item distinct while asking for exactly the same expensive work.
func TestAuditD_SecA_ManyItemsOnOneBillAreNotDuplicates(t *testing.T) {
	limits := transport.DefaultLimits()
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)

	target := strings.Repeat("cc", 32)
	items := make([]transport.Item, 0, limits.MaxItems)
	for i := 0; i < limits.MaxItems; i++ {
		priv := nostr.GeneratePrivateKey()
		pub, err := nostr.GetPublicKey(priv)
		require.NoError(t, err)
		attestation, _ := auditDSecAAttestation(t, priv, pub)
		params, err := json.Marshal(map[string]any{
			"scope":             "mine",
			"attestation_event": attestation,
		})
		require.NoError(t, err)
		items = append(items, transport.Item{
			ID: fmt.Sprintf("i%d", i), Target: target,
			Method: constants.NIP47MethodCashStatus, Params: params,
			Proof:     json.RawMessage(`{"kind":23192}`),
			BillProof: json.RawMessage(`{"kind":23193}`),
		})
	}

	env := transport.Envelope{
		Version:  transport.EnvelopeVersion,
		NotAfter: time.Now().Add(60 * time.Second).Unix(),
		Nonce:    nonce, ReplyTo: replyTo, Items: items,
	}
	plaintext, err := env.Encode(limits)
	require.NoError(t, err,
		"%d items naming one bill must be a legal envelope — distinct params, distinct hashes", limits.MaxItems)

	decoded, err := transport.Decode(plaintext, limits)
	require.NoError(t, err, "and the hub must accept it, budget included")
	require.Len(t, decoded.Items, limits.MaxItems)

	total := 0
	for _, item := range decoded.Items {
		total += item.VerificationCost()
	}
	t.Logf("accepted: %d bytes, %d items on ONE bill, believed cost %d of the %d budget",
		len(plaintext), len(decoded.Items), total, limits.MaxVerifyBudget)
	assert.LessOrEqual(t, total, limits.MaxVerifyBudget)
}
