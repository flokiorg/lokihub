package nip47

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// TestHandleEvent_UnknownApp_SilentlyDropsWithNoResponse is the UX-audit
// counterpart to the pre-existing TODO in this file ("test if an app doesn't
// exist it returns the right error code" — see doTestCreateResponse's
// neighboring tests). It documents the ACTUAL current behavior: there is no
// error code at all. HandleEvent's app lookup (svc.db.Where("app_pubkey =
// ?", ...).First(&app)) simply logs and returns when no row matches — no
// nostr response event is ever published.
//
// This matters for the Cash Hub lifecycle specifically because a cash_wallet
// is deleted outright (not just marked expired) by the periodic expiry sweep
// (service.runCashCleanup, every 5 minutes — see
// service/cash_cleanup_service.go) once its ExpiresAt has passed. In the
// window between ExpiresAt and the sweep actually running, a recipient's
// cash_redeem/cash_transfer/list_recipients call against that wallet
// correctly gets a clear, actionable constants.ERROR_EXPIRED ("This app has
// expired") — see permissions.HasPermission and
// TestHandleEvent_CashWallet_ClaimFunds_RejectedWhenWalletExpired in
// event_handler_test.go. But once the sweep has actually deleted the App
// row, the SAME recipient retrying the SAME call gets nothing back at all:
// indistinguishable, from the caller's side, from a relay outage, a dropped
// packet, or a wrong/corrupted pairing secret. A recipient (or a third-party
// client author) who took too long to redeem has no way to learn "this
// wallet is gone for good" — only silence, forever, with no error code to
// branch on.
//
// The same silent-drop path is also what a brand-new recipient hits if they
// mistype or otherwise corrupt a lokicash1... token's wallet_pubkey before
// ever successfully connecting: nothing in this codebase distinguishes "this
// connection has never existed" from "this connection expired" from "the
// relay ate my request".
func TestHandleEvent_UnknownApp_SilentlyDropsWithNoResponse(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)

	// A request signed by a pubkey that was never registered as any app's
	// app_pubkey — simulating either a full-drain/expiry-swept cash_wallet, or
	// a recipient who simply has the wrong connection.
	reqPrivateKey := nostr.GeneratePrivateKey()
	reqPubkey, err := nostr.GetPublicKey(reqPrivateKey)
	require.NoError(t, err)

	reqEvent := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    reqPubkey,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2}},
		// Content doesn't even need to decrypt to anything meaningful — the
		// app lookup happens, and fails, before content is ever decrypted.
		Content: "irrelevant-undecryptable-content",
	}
	require.NoError(t, reqEvent.Sign(reqPrivateKey))

	pool := tests.NewMockSimplePool()
	ctx := context.TODO()

	nip47svc.HandleEvent(ctx, pool, reqEvent, svc.LNClient)

	// This is the crux of the finding: NOT a NIP-47 error response (which
	// would at least tell a caller something happened), but literally zero
	// published events. A well-behaved third-party client has no protocol
	// signal to distinguish "your wallet's gone" from "try again later".
	require.Empty(t, pool.PublishedEvents,
		"expected no NIP-47 response to be published for an unknown app pubkey — "+
			"HandleEvent currently drops this case silently instead of returning any error code")
}
