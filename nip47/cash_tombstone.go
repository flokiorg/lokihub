package nip47

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	nostrmodels "github.com/flokiorg/lokihub/nostr/models"
	"github.com/ohstr/nmilat/nipcash"
)

// tryReplySpentBill answers a request naming a cash bill this hub destroyed,
// with a "spent" tombstone carrying the deadline past which the hub falls
// silent again (NIP-CASH §Cash Status, db.CashHubConfig.SpentRetentionSecs).
//
// It exists because silence alone cannot be told apart from a hub that is slow
// or unreachable, which forces every client to pick a wrong answer: treat the
// timeout as retryable and a genuinely spent bill retries forever, or treat it
// as gone and an outage tells someone their funds are lost.
//
// Reports whether it answered. Every "no" path leaves the caller's original
// silence untouched — this only ever converts silence into an answer, never an
// answer into something else.
//
// The privacy property survives intact. Three gates have to pass:
//
//  1. the wallet pubkey names a bill THIS hub archived;
//  2. the hub's own retention window has not elapsed;
//  3. the requester's pubkey is the one derived from that bill's own pairing
//     key — so only someone who held the bill's token gets an answer, and a
//     pubkey the hub never served is still met with silence, exactly as
//     NIP-CASH §Archival on Deletion requires.
//
// Gate 3 is the load-bearing one: without it this would become the existence
// oracle the deletion exists to remove.
func (svc *nip47Service) tryReplySpentBill(ctx context.Context, pool nostrmodels.SimplePool, event *nostr.Event, requestEvent *db.RequestEvent, walletPubkey string) bool {
	if walletPubkey == "" {
		return false
	}

	// Gate 2. The boundary comparison is db.RetentionWindowOpen's, never a local
	// one: this path and the relay gate (service.walletRegistry, via
	// db.SpentBillStillAnswerable) must agree exactly, and when each wrote the
	// comparison itself they did not — at exactly retainedUntil the gate admitted
	// the request and this function declined to answer it, so the request was
	// accepted and then dropped in silence. See db.RetentionWindowOpen.
	//
	// The deadline is read here rather than calling SpentBillStillAnswerable
	// because the response carries it as retained_until, so it is needed either
	// way and a second query would be wasted.
	retainedUntil, ok := db.SpentBillRetainedUntil(svc.db, walletPubkey)
	if !ok || !db.RetentionWindowOpen(retainedUntil, time.Now()) {
		return false
	}

	var bill db.CashBillArchive
	if err := svc.db.Where("wallet_pubkey = ?", walletPubkey).First(&bill).Error; err != nil {
		return false
	}

	// Gate 3. The bill's client pairing key is deterministic from its app id,
	// so the pubkey that key produces is the only one that ever legitimately
	// addressed this bill.
	pairingKey, err := svc.keys.GetCashPairingKey(bill.WalletAppID)
	if err != nil {
		logger.Logger.Error().Err(err).
			Uint("walletAppId", bill.WalletAppID).
			Msg("Failed to derive a spent bill's pairing key")
		return false
	}
	expectedPubkey, err := nostr.GetPublicKey(pairingKey)
	if err != nil || expectedPubkey != event.PubKey {
		return false
	}

	walletPrivKey, err := svc.keys.GetAppWalletKey(bill.WalletAppID)
	if err != nil {
		logger.Logger.Error().Err(err).
			Uint("walletAppId", bill.WalletAppID).
			Msg("Failed to derive a spent bill's wallet key")
		return false
	}

	encryption := constants.ENCRYPTION_TYPE_NIP04
	if tag := event.Tags.Find("encryption"); tag != nil {
		encryption = tag[1]
	}
	nip47Cipher, err := cipher.NewNip47Cipher(encryption, event.PubKey, walletPrivKey)
	if err != nil {
		return false
	}

	// Only the status read gets a tombstone. A cash_redeem or cash_transfer
	// naming a destroyed bill keeps today's silence: the agreed design is
	// three outcomes on the STATUS method, and answering the others would both
	// return a result_type that does not match the request and widen the
	// oracle past what was asked for — the existing replay tests spell out why
	// silence matters there.
	//
	// The method only becomes visible once the request is decrypted, which is
	// why this check sits below the cipher rather than at the top.
	payload, err := nip47Cipher.Decrypt(event.Content)
	if err != nil {
		return false
	}
	nip47Request := &models.Request{}
	if err := json.Unmarshal([]byte(payload), nip47Request); err != nil {
		return false
	}
	if nip47Request.Method != nipcash.MethodCashStatus {
		return false
	}

	deadline := retainedUntil.Unix()
	resp, err := svc.CreateResponse(event, &models.Response{
		// Echo the method the caller actually used, so a client still on the
		// old name gets a result_type it recognises.
		ResultType: nip47Request.Method,
		Result: nipcash.CashStatusResult{
			Error:         nipcash.ErrorSpent,
			RetainedUntil: &deadline,
		},
	}, nostr.Tags{}, nip47Cipher, walletPrivKey)
	if err != nil {
		logger.Logger.Error().Err(err).
			Str("walletPubkey", walletPubkey).
			Msg("Failed to build a spent-bill reply")
		return false
	}

	// app is nil: the row is gone, which is the whole point.
	if err := svc.publishResponseEvent(ctx, pool, requestEvent, resp, nil); err != nil {
		logger.Logger.Error().Err(err).
			Str("walletPubkey", walletPubkey).
			Msg("Failed to publish a spent-bill reply")
		return false
	}

	logger.Logger.Debug().
		Str("walletPubkey", walletPubkey).
		Time("retainedUntil", retainedUntil).
		Msg("Answered a request for a spent cash bill")
	return true
}
