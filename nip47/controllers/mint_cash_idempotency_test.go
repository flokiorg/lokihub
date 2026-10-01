package controllers

// mint_cash was the one money method with no replay guard, tracked in
// data/docs/issues/mint-cash-no-idempotency-key-2026-09-21.md.
//
// cash_transfer and cash_consolidate sources carry a signed identity_event the backend
// refuses on replay. mint_cash's params are plain identity_type / identity_value /
// amount_millis with no nonce, so a caller whose retry logic reads a timeout as "it
// failed" resends the same logical request as a new NWC event and the Hub mints and
// funds a SECOND wallet.
//
// The timeout does not mean the mint did not happen: Commit is durable and complete
// before any response is built, and the response row is recorded PUBLISH_CONFIRMED or
// PUBLISH_FAILED either way. The observed 2026-09-21 case was a response sitting
// confirmed on the relay for a subscriber that had already given up waiting.
//
// A replay is REFUSED rather than answered with the original result, which is the
// honest choice and not a shortcut: for a cash-mode mint the secret exists only in the
// reply the caller missed — the Hub keeps a commitment, never the secret — so there is
// nothing to replay. Storing secrets to make replay work would trade this gap for a
// strictly worse one. What the caller gets is the identity of the wallet it already
// created, so it can read that wallet's state instead of guessing.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func mintRequestWithKey(pubkey string, amountMloki uint64, expirationSecs int, key string) string {
	idem := ""
	if key != "" {
		idem = fmt.Sprintf(`, "idempotency_key": %q`, key)
	}
	return fmt.Sprintf(`{
		"method": "mint_cash",
		"params": { "recipients": [%s], "expiry": %d%s }
	}`, onePubkeyRecipientJSON(pubkey, amountMloki), expirationSecs, idem)
}

// seedTwoPayableInvoices queues the only two distinct, really-payable bolt11 fixtures
// this mock has, so a test can mint TWICE against one database.
//
// Needed because the mock's MakeInvoice otherwise returns one constant invoice every
// time, and the transactions service's "already paid" guard is keyed on the real
// decoded payment_hash — so a second mint in the same DB fails with
// "this invoice has already been paid" rather than for any reason the test is about.
// That is the documented single-payee-per-test wall (see
// cashwallet/consolidate_rollback_test.go's doc comment), and two is the ceiling:
// every canned invoice has a distinct payee baked into its real signature.
//
// A third mint in one DB needs another real bolt11 fixture, not a workaround.
func seedTwoPayableInvoices(t *testing.T, svc *tests.TestService) {
	t.Helper()
	mockLn, ok := svc.LNClient.(*tests.MockLn)
	require.True(t, ok)
	mockLn.MakeInvoiceQueue = []*lnclient.Transaction{
		{
			Type: "incoming", Invoice: tests.MockInvoice,
			PaymentHash: tests.MockPaymentHash, Preimage: "idem-preimage-1", Amount: 1000,
		},
		{
			Type: "incoming", Invoice: tests.MockZeroAmountInvoice,
			PaymentHash: tests.MockZeroAmountPaymentHash, Preimage: "idem-preimage-2", Amount: 1000,
		},
	}
}

func mintOnce(t *testing.T, svc *tests.TestService, hub *db.App, pubkey string, key string) *models.Response {
	t.Helper()
	req := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(mintRequestWithKey(pubkey, 1000, 3600, key)), req))
	ev := &db.RequestEvent{}
	svc.DB.Create(&ev)

	var got *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(context.TODO(), req, ev.ID, hub, func(r *models.Response, _ nostr.Tags) {
		got = r
	})
	require.NotNil(t, got, "the controller published nothing")
	return got
}

func cashWalletCount(t *testing.T, svc *tests.TestService, hub *db.App) int64 {
	t.Helper()
	var n int64
	require.NoError(t, svc.DB.Model(&db.App{}).
		Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Count(&n).Error)
	return n
}

func TestMintCash_SameIdempotencyKey_MintsOnce(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 1_000_000, "idem-fund")
	beneficiary, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	first := mintOnce(t, svc, hub, beneficiary, "retry-me-once")
	require.Nil(t, first.Error, "the first mint must succeed")
	require.Equal(t, int64(1), cashWalletCount(t, svc, hub))

	// The retry. Pre-fix this minted and funded a second wallet.
	second := mintOnce(t, svc, hub, beneficiary, "retry-me-once")
	require.NotNil(t, second.Error, "the retry was served again — this is the double mint")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, second.Error.Code)
	assert.Equal(t, int64(1), cashWalletCount(t, svc, hub),
		"a second cash wallet was created and funded for one logical request")

	// The refusal must name the wallet the caller already has, or it is useless to a
	// caller trying to recover.
	var record db.CashMintIdempotency
	require.NoError(t, svc.DB.Where("hub_app_id = ?", hub.ID).First(&record).Error)
	assert.Contains(t, second.Error.Message, record.WalletPubkey,
		"the refusal must identify the wallet already minted")
	assert.Contains(t, second.Error.Message, "cash_status",
		"the refusal must name the recovery path, since the original secret cannot be reissued")
}

func TestMintCash_DifferentKeys_MintSeparately(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 1_000_000, "idem-fund-2")
	seedTwoPayableInvoices(t, svc)
	beneficiary, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	require.Nil(t, mintOnce(t, svc, hub, beneficiary, "key-a").Error)
	require.Nil(t, mintOnce(t, svc, hub, beneficiary, "key-b").Error)
	assert.Equal(t, int64(2), cashWalletCount(t, svc, hub),
		"two distinct keys are two distinct requests; deduping them would break a "+
			"caller legitimately minting the same amount to the same recipient twice")
}

func TestMintCash_NoKey_IsUnchanged(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 1_000_000, "idem-fund-3")
	seedTwoPayableInvoices(t, svc)
	beneficiary, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// The field is optional and omitted by every existing caller. Two identical
	// keyless requests must still mint twice — the guard is opt-in, so it cannot
	// change behaviour for anyone who has not asked for it.
	require.Nil(t, mintOnce(t, svc, hub, beneficiary, "").Error)
	require.Nil(t, mintOnce(t, svc, hub, beneficiary, "").Error)
	assert.Equal(t, int64(2), cashWalletCount(t, svc, hub))

	var n int64
	require.NoError(t, svc.DB.Model(&db.CashMintIdempotency{}).Count(&n).Error)
	assert.Zero(t, n, "no key, no record — the table must not accumulate rows for callers not using it")
}

func TestMintCash_KeyIsScopedPerHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hubA := tests.CreateCashHub(t, svc, 100_000, 3600)
	hubB := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hubA.ID, 1_000_000, "idem-hub-a")
	tests.FundApp(svc, hubB.ID, 1_000_000, "idem-hub-b")
	seedTwoPayableInvoices(t, svc)
	beneficiary, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// Two hubs are two ledgers. One hub's key must not block another's, or a shared
	// key generator (a timestamp, a counter) would make hubs interfere.
	require.Nil(t, mintOnce(t, svc, hubA, beneficiary, "shared-key").Error)
	require.Nil(t, mintOnce(t, svc, hubB, beneficiary, "shared-key").Error,
		"hub B refused a key only hub A had seen")
	assert.Equal(t, int64(1), cashWalletCount(t, svc, hubA))
	assert.Equal(t, int64(1), cashWalletCount(t, svc, hubB))
}

// TestMintCash_ConcurrentSameKey_ExactlyOneWins is the row that matters most, because
// the read-then-insert in the controller is NOT the guard — the unique index is. A
// caller retrying on a timeout races its own original request by construction: the
// first attempt is still in flight, which is exactly why it timed out.
func TestMintCash_ConcurrentSameKey_ExactlyOneWins(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 5_000_000, "idem-concurrent")
	beneficiary, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	const attempts = 4
	responses := make([]*models.Response, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := &models.Request{}
			if uErr := json.Unmarshal([]byte(mintRequestWithKey(beneficiary, 1000, 3600, "racing-retry")), req); uErr != nil {
				return
			}
			ev := &db.RequestEvent{}
			svc.DB.Create(&ev)
			NewTestNip47Controller(svc).HandleMintCashEvent(context.TODO(), req, ev.ID, hub,
				func(r *models.Response, _ nostr.Tags) { responses[i] = r })
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, r := range responses {
		if r != nil && r.Error == nil {
			succeeded++
		}
	}

	// The invariant is money, not the response count: however the race resolves, the
	// hub must not have funded more wallets than requests it accepted.
	wallets := cashWalletCount(t, svc, hub)
	assert.LessOrEqual(t, wallets, int64(succeeded),
		"%d wallets exist for %d accepted requests — the guard let a duplicate through",
		wallets, succeeded)
	assert.GreaterOrEqual(t, succeeded, 1, "every concurrent attempt was refused; at least one must win")
	assert.LessOrEqual(t, wallets, int64(1),
		"one logical request minted %d wallets", wallets)
	t.Logf("%d/%d attempts accepted, %d wallet(s) minted", succeeded, attempts, wallets)
}

// TestMintCash_FailedMint_ReleasesTheKey is the flaw the atomic version introduced and
// this closes: the key commits in the same transaction as the wallet, but FUNDING
// happens after that transaction and is not part of it. When funding fails the wallet
// is compensated away — so a key left behind would refuse the caller's legitimate retry
// forever, naming a wallet that no longer exists.
//
// A guard that blocks the retry of a FAILED mint is worse than no guard: it turns a
// transient funding failure into a permanently unusable key.
//
// The failure has to land AFTER the transaction, which is the whole point and is what
// the first version of this test got wrong: it used an unfunded hub, and an unfunded
// hub fails at Resolve — step 5, the balance check — before any transaction opens, so
// no key was ever written and the test passed against the broken code too. The mock's
// single-payable-invoice wall gives the right shape instead: the ledger says the hub
// can pay, so Resolve passes, and the internal transfer then fails on the real decoded
// payment_hash of an invoice already spent by the first mint.
func TestMintCash_FailedMint_ReleasesTheKey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 1_000_000, "release-fund")
	beneficiary, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// Burn the only payable invoice, so the NEXT mint gets past Resolve and then
	// fails on the transfer.
	require.Nil(t, mintOnce(t, svc, hub, beneficiary, "").Error, "the first mint must succeed")

	failed := mintOnce(t, svc, hub, beneficiary, "key-for-a-doomed-mint")
	require.NotNil(t, failed.Error, "this test needs the second mint to fail")
	require.Contains(t, failed.Error.Message, "already been paid",
		"the failure must be the post-transaction funding one, not a pre-transaction "+
			"validation one — otherwise no key was ever written and this proves nothing")

	// The wallet was compensated away, so only the first mint's wallet remains.
	assert.Equal(t, int64(1), cashWalletCount(t, svc, hub),
		"the failed mint's wallet must be rolled back")

	var n int64
	require.NoError(t, svc.DB.Model(&db.CashMintIdempotency{}).
		Where("idempotency_key = ?", "key-for-a-doomed-mint").Count(&n).Error)
	assert.Zero(t, n, "the key survived a mint that was rolled back, so the caller can never retry it")
}
