package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// feeReserveHeadroomMloki covers CalculateFeeReserveMloki's floor
// (max(1% of amount, 10_000 mloki) — see transactions_service.go) for a
// non-self-payment. claim_funds's own "invoice amount must exactly equal the
// claimed slice" rule is what these tests exercise; the wallet's real
// isolated balance (and budget cap) separately need this much headroom above
// the exact slice total so a genuine external-payee payout doesn't spuriously
// fail validateCanPay's pre-flight balance/quota checks — same fee-reserve-
// floor trap documented for this codebase's other full-drain tests.
const feeReserveHeadroomMloki = 50_000

// newFundedJITWallet creates a jit_wallet child directly (bypassing
// jitwallet.Create, whose mocked funding-transfer amount fidelity isn't
// reliable for these tests — see tests.FundApp's doc comment) and seeds its
// ledger balance (and budget cap) at totalMloki plus fee-reserve headroom, so
// a real claim_funds payout of exactly totalMloki doesn't spuriously fail the
// balance/quota pre-checks on top of the exact slice amount.
func newFundedJITWallet(t *testing.T, svc *tests.TestService, hub *db.App, totalMloki int64) *db.App {
	t.Helper()
	funded := uint64(totalMloki) + feeReserveHeadroomMloki
	wallet, _, err := svc.AppsService.CreateApp(
		"jit-wallet", "", funded/1000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindJITWallet, &hub.ID, db.ParentKindJIT, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, funded, tests.RandomHex32())
	return wallet
}

// buildClaimProofEvent builds and signs a kind-35521 claim proof bound to
// walletPubkey and bolt11Hash. extraTags carries the connection_key-mode-only
// tags (connection_key + e-tag referencing the attestation event).
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

func buildIAAttestationEvent(t *testing.T, iaPrivkey, connectionKey, claimantNostrPubkey string, expiration *int64) *nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"d", connectionKey}, {"p", claimantNostrPubkey}}
	if expiration != nil {
		tags = append(tags, nostr.Tag{"expiration", strconv.FormatInt(*expiration, 10)})
	}
	ev := &nostr.Event{
		Kind:      nostrKindIAAttestation,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	require.NoError(t, ev.Sign(iaPrivkey))
	return ev
}

func mustMarshal(t *testing.T, ev *nostr.Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	return string(b)
}

// handleClaimFundsFor dispatches HandleClaimFundsEvent against app and
// returns the decoded response. Creates a fresh RequestEvent row each call —
// SendPaymentSync's Transaction row has a real FK to request_events, so a
// literal placeholder ID fails once a call actually reaches the payment path.
func handleClaimFundsFor(t *testing.T, svc *tests.TestService, controller *nip47Controller, app *db.App, params claimFundsParams) *models.Response {
	t.Helper()
	content := map[string]interface{}{
		"method": constants.NIP47MethodClaimFunds,
		"params": params,
	}
	reqBytes, _ := json.Marshal(content)
	nip47Request := &models.Request{}
	_ = json.Unmarshal(reqBytes, nip47Request)

	dbRequestEvent := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	var response *models.Response
	controller.HandleClaimFundsEvent(context.TODO(), nip47Request, dbRequestEvent.ID, app, func(r *models.Response, _ nostr.Tags) {
		response = r
	}, nostr.Tags{})
	return response
}

func TestHandleClaimFundsEvent_HappyPath_PubkeyMode(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.Nil(t, response.Error)
	result := response.Result.(payResponse)
	assert.NotEmpty(t, result.Preimage)

	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	assert.Nil(t, claim, "slice must show as claimed (no longer returned by the unclaimed-only lookup)")
}

func TestHandleClaimFundsEvent_HappyPath_ConnectionKeyMode(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 2000)

	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	registerTrustedIA(t, svc, iaPubkey)
	connectionKey := tests.RandomHex32()
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPubkey, AmountMloki: 2000},
	}))

	attestation := buildIAAttestationEvent(t, iaPrivkey, connectionKey, claimantPubkey, oneHourFromNow())
	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:          tests.MockZeroAmountInvoice,
		Amount:           ptrUint64(2000),
		IdentityType:     db.JITAllocIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, proof),
		AttestationEvent: mustMarshal(t, attestation),
	})

	require.Nil(t, response.Error)
}

// TestHandleClaimFundsEvent_ProofBoundToDifferentInvoice_Rejected is the core
// audit-finding coverage: since the wallet's connection may be shared/public,
// anyone holding it can decrypt every claim_funds request sent on it — an
// attacker who intercepts a valid proof must not be able to redirect the
// payout to a different invoice by resubmitting it with their own.
func TestHandleClaimFundsEvent_ProofBoundToDifferentInvoice_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 123_000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 123_000},
	}))

	// Proof is bound to MockPaymentHash (i.e. tests.MockInvoice)...
	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockPaymentHash, nil, time.Now())

	// ...but the attacker submits it against a DIFFERENT invoice (MockZeroAmountInvoice).
	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(123_000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// The slice must remain unclaimed — the attacker gained nothing.
	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim)
}

func TestHandleClaimFundsEvent_ReplaySameProofSameInvoice_AfterSuccess_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	params := claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	}

	controller := NewTestNip47Controller(svc)
	first := handleClaimFundsFor(t, svc, controller, wallet, params)
	require.Nil(t, first.Error)

	// Exact same request (same valid proof, same invoice) replayed — must be
	// a no-op failure, not a second payout.
	second := handleClaimFundsFor(t, svc, controller, wallet, params)
	require.NotNil(t, second.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, second.Error.Code)
}

// TestHandleClaimFundsEvent_SettleRacesFailure_NotDoubleClaimable is the
// claim_funds-level regression test for the settle-vs-fail fund-drain path
// (see transactions.TestSendPaymentSync_SettleRacesFailure_ReturnsSettled for
// the lower-level version of the same race): the payout's underlying
// SendPaymentSync call is delayed and configured to ultimately error, while
// an async "nwc_lnclient_payment_sent" event (the same kind a real node
// subscription would emit) settles the same payment hash first. Before the
// fix, this made claim_funds_controller's error branch call
// UnclaimJITWalletSlice on a slice whose payout had actually already
// succeeded, reopening it for a second, real payout - a double-spend of the
// JIT hub's funds. With the fix, SendPaymentSync returns the settled
// transaction instead of the stale RPC error, so the controller's error
// branch (and the unclaim it would have triggered) is never reached.
func TestHandleClaimFundsEvent_SettleRacesFailure_NotDoubleClaimable(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	params := claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	}

	delay := 100 * time.Millisecond
	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.PaymentDelay = &delay
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, errors.New("timeout talking to node"))
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, nil)

	controller := NewTestNip47Controller(svc)

	var wg sync.WaitGroup
	var response *models.Response
	wg.Add(1)
	go func() {
		defer wg.Done()
		response = handleClaimFundsFor(t, svc, controller, wallet, params)
	}()

	// Wait for the goroutine to actually create the PENDING row before firing
	// the settle event - a fixed sleep here is flaky under load (e.g. running
	// the full test suite in parallel), since it races the same 100ms mock
	// delay this test depends on to open the window in the first place.
	require.Eventually(t, func() bool {
		var pending db.Transaction
		return svc.DB.Where(&db.Transaction{
			Type:        constants.TRANSACTION_TYPE_OUTGOING,
			State:       constants.TRANSACTION_STATE_PENDING,
			PaymentHash: tests.MockZeroAmountPaymentHash,
		}).First(&pending).Error == nil
	}, 2*time.Second, 2*time.Millisecond, "PENDING transaction row was never created")

	// Simulate the async payment-sent-event subscription winning the race and
	// settling the transaction before the delayed synchronous error above returns.
	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	transactionsSvc.ConsumeEvent(context.TODO(), &events.Event{
		Event: "nwc_lnclient_payment_sent",
		Properties: &lnclient.Transaction{
			Type:        "outgoing",
			Invoice:     tests.MockZeroAmountInvoice,
			Preimage:    tests.RandomHex32(),
			PaymentHash: tests.MockZeroAmountPaymentHash,
			Amount:      1000,
			FeesPaid:    0,
		},
	}, nil)

	wg.Wait()

	require.Nil(t, response.Error, "the payout actually settled - claim_funds must not surface the stale RPC error")
	result := response.Result.(payResponse)
	assert.NotEmpty(t, result.Preimage)

	// The bug this guards against: the slice must NOT have been reopened by
	// an incorrect UnclaimJITWalletSlice call, so a second claim attempt for
	// the same identity must be rejected exactly like the ordinary
	// already-claimed replay case, not paid out a second time.
	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	assert.Nil(t, claim, "slice must show as claimed, not reopened by the settle race")

	replay := handleClaimFundsFor(t, svc, controller, wallet, params)
	require.NotNil(t, replay.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, replay.Error.Code)
}

func TestHandleClaimFundsEvent_ForgedIdentity_NoMatchingSlice_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	// A real recipient exists on the wallet...
	realPrivkey := nostr.GeneratePrivateKey()
	realPubkey, _ := nostr.GetPublicKey(realPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: realPubkey, AmountMloki: 1000},
	}))

	// ...but an outsider (who genuinely owns their own key — the signature is
	// perfectly valid) tries to claim under their OWN identity, which has no
	// slice on this wallet.
	outsiderPrivkey := nostr.GeneratePrivateKey()
	outsiderPubkey, _ := nostr.GetPublicKey(outsiderPrivkey)
	proof := buildClaimProofEvent(t, outsiderPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: outsiderPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, response.Error.Code)
}

func TestHandleClaimFundsEvent_IdentityEventReplayedAcrossWallets_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	walletA := newFundedJITWallet(t, svc, hub, 1000)
	walletB := newFundedJITWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	// Same identity happens to have a slice on both wallets.
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(walletA.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(walletB.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))

	// Proof is bound (d-tag) to wallet A's pubkey...
	proof := buildClaimProofEvent(t, claimantPrivkey, *walletA.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())

	// ...but submitted against wallet B.
	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), walletB, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// Wallet A's own slice must remain untouched/claimable — this wasn't a valid claim there either.
	claimOnA, err := svc.AppsService.GetJITWalletClaim(walletA.ID, db.JITAllocIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	require.NotNil(t, claimOnA)
}

func TestHandleClaimFundsEvent_AmountMismatch_RejectedAndSliceRemainsClaimable(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	controller := NewTestNip47Controller(svc)

	// Request only half the entitled amount — must be rejected outright, not
	// accepted as a valid partial claim.
	response := handleClaimFundsFor(t, svc, controller, wallet, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(500),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// The slice must be claimable again — a bad invoice attempt didn't burn it.
	claim, err := svc.AppsService.GetJITWalletClaim(wallet.ID, db.JITAllocIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "an amount-mismatch attempt must roll back the claim, not consume it")

	// A fresh, correctly-amounted attempt (bound to the same invoice) must succeed.
	retryResponse := handleClaimFundsFor(t, svc, controller, wallet, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})
	require.Nil(t, retryResponse.Error)
}

func TestHandleClaimFundsEvent_StaleIdentityEvent_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))

	staleProof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil,
		time.Now().Add(-1*time.Hour))

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, staleProof),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

func TestHandleClaimFundsEvent_ConnectionKeyMode_UntrustedIA_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	realIAPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	registerTrustedIA(t, svc, realIAPubkey)
	imposterIAPrivkey := nostr.GeneratePrivateKey() // signs the attestation, but isn't the recorded IA for this slice
	connectionKey := tests.RandomHex32()
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: realIAPubkey, AmountMloki: 1000},
	}))

	attestation := buildIAAttestationEvent(t, imposterIAPrivkey, connectionKey, claimantPubkey, oneHourFromNow())
	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:          tests.MockZeroAmountInvoice,
		Amount:           ptrUint64(1000),
		IdentityType:     db.JITAllocIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, proof),
		AttestationEvent: mustMarshal(t, attestation),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

// TestHandleClaimFundsEvent_ConnectionKeyMode_AttestationForDifferentClaimant_Rejected
// covers a distinct attack from the UntrustedIA case above: here the
// attestation is genuine — signed by the correct, trusted IA recorded for
// this exact slice, for the exact connection_key on this wallet — but it was
// issued to a real claimant. Since a kind-35522 attestation is itself a
// signed, relayable nostr event (not a secret), an attacker who intercepts
// or discovers one has everything except the real claimant's private key.
// The attacker signs their OWN claim proof and tries to redirect the payout
// to themselves by reusing the real claimant's attestation. Must be rejected
// on the attestation's p-tag (claimant binding), and the slice must remain
// claimable by the real claimant afterward.
func TestHandleClaimFundsEvent_ConnectionKeyMode_AttestationForDifferentClaimant_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	iaPrivkey := nostr.GeneratePrivateKey()
	iaPubkey, _ := nostr.GetPublicKey(iaPrivkey)
	registerTrustedIA(t, svc, iaPubkey)
	connectionKey := tests.RandomHex32()

	realClaimantPrivkey := nostr.GeneratePrivateKey()
	realClaimantPubkey, _ := nostr.GetPublicKey(realClaimantPrivkey)

	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPubkey, AmountMloki: 1000},
	}))

	// A genuine attestation, correctly signed by the correct IA, for the real claimant.
	attestation := buildIAAttestationEvent(t, iaPrivkey, connectionKey, realClaimantPubkey, oneHourFromNow())

	// An attacker with no relationship to that identity intercepts it and
	// tries to claim by signing the proof with their OWN key instead.
	attackerPrivkey := nostr.GeneratePrivateKey()
	attackerProof := buildClaimProofEvent(t, attackerPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:          tests.MockZeroAmountInvoice,
		Amount:           ptrUint64(1000),
		IdentityType:     db.JITAllocIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, attackerProof),
		AttestationEvent: mustMarshal(t, attestation),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)

	// The slice must remain intact and claimable by the real claimant.
	realProof := buildClaimProofEvent(t, realClaimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())
	retryResponse := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, claimFundsParams{
		Invoice:          tests.MockZeroAmountInvoice,
		Amount:           ptrUint64(1000),
		IdentityType:     db.JITAllocIdentityConnectionKey,
		IdentityValue:    connectionKey,
		IdentityEvent:    mustMarshal(t, realProof),
		AttestationEvent: mustMarshal(t, attestation),
	})
	require.Nil(t, retryResponse.Error, "the real claimant's own claim must still succeed after the attacker's attempt was rejected")
}

func TestHandleClaimFundsEvent_RateLimited(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000},
	}))

	controller := NewTestNip47Controller(svc)
	for i := 0; i < jitClaimRateLimitPerHour; i++ {
		controller.jitClaimLimiter.Allow(wallet.AppPubkey, jitClaimRateLimitPerHour)
	}

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	response := handleClaimFundsFor(t, svc, controller, wallet, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, response.Error.Code)
}

func TestHandleClaimFundsEvent_NonJITWalletApp_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)

	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), hub, claimFundsParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(1000),
		IdentityType:  db.JITAllocIdentityPubkey,
		IdentityValue: tests.RandomHex32(),
		IdentityEvent: "{}",
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

func ptrUint64(v uint64) *uint64 { return &v }

// oneHourFromNow is a valid expiration timestamp for attestations built in
// tests that aren't specifically testing expiration — verifyClaimAttestationEvent
// requires the tag to be present, so tests can't pass nil to skip it anymore.
func oneHourFromNow() *int64 {
	exp := time.Now().Add(time.Hour).Unix()
	return &exp
}
