package cashwallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/ledger"
	"github.com/flokiorg/lokihub/logger"
)

// Stages of the commit at which TestHooks.Before runs. A source claim, transfer
// or proof stage carries its index, e.g. "AfterTransfer(1)".
const (
	StageAfterLock        = "AfterLock"
	StageAfterCreateApp   = "AfterCreateApp"
	StageAfterSign        = "AfterSign"
	StageAfterClaims      = "AfterClaims"
	StageAfterSourceClaim = "AfterSourceClaim"
	StageAfterReassign    = "AfterReassign"
	StageAfterTransfer    = "AfterTransfer"
	StageAfterProof       = "AfterProof"
	StageAfterRecord      = "AfterRecord"
	StageBeforeCommit     = "BeforeCommit"
)

// TestHooks is the commit's fault-injection seam. Before runs at every stage; an
// error it returns rolls the commit back, and a panic abandons the transaction
// the way a crash would. nil in production.
type TestHooks struct {
	Before func(stage string) error
}

// ErrProofUsed: a claim proof of this call was consumed by another call that
// committed first (NIP-CASH §Claim Proofs: UNAUTHORIZED, a proof already used).
var ErrProofUsed = errors.New("a claim proof of this call was already used")

// ClaimProof is one claim proof a call consumes when, and only when, it commits.
type ClaimProof struct {
	EventID   string
	CreatedAt int64
	Method    string
}

func (d Deps) before(stage string) error {
	if d.Hooks == nil || d.Hooks.Before == nil {
		return nil
	}
	return d.Hooks.Before(stage)
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) requests() cashrequest.Store {
	return cashrequest.Store{DB: d.DB, Now: d.now}
}

// newBill is one bill a commit creates.
type newBill struct {
	role             string // its record role (db.CashRecordBill*)
	nameSeed         string
	amount           uint64
	expiresAt        *time.Time
	claims           []db.CashWalletClaim
	identityRequired bool
	splitFrom        *uint
	// lineageRoot is the bill's lineage root; nil makes the bill its own root.
	lineageRoot *uint
	// zap, when set, makes this the bill of a zap transfer: signed under the zap
	// scheme with issued_at taken from the clock inside the commit.
	zap *zapBill
}

// zapBill is what a zap bill signs beside its amount (NIP-CASH §Mint Provenance,
// scheme 2): recipient and sender pubkeys, the zap_target (signed as its sha256,
// never stored) and the expiry inherited from the source, 0 for never.
type zapBill struct {
	recipient, sender, target string
	expiresAt                 uint64
}

type internalTransfer struct {
	fromAppID uint
	toBill    int
	amount    uint64
}

type reassignment struct {
	ref                                         apps.SliceRef
	newIdentityType, newIdentityValue, newIAKey string
}

// commitPlan is everything one call writes. Every write happens in one database
// transaction, so the call either commits whole or leaves nothing (NIP-CASH
// §Atomicity — One Commit); only its request_id mark, taken before, survives.
type commitPlan struct {
	op        string
	hub       *db.App
	lockApps  []uint
	bills     []newBill
	sources   []apps.SliceRef // claimed terminal; their value moved into bills[0]
	reassign  *reassignment
	transfers []internalTransfer
	proofs    []ClaimProof
	// record, when set, is written last, under the call's deadline.
	record *db.CashRequestRecord
}

// committedBill is a bill a commit created, and the token it was issued with;
// ZapIssuedAt and ZapExpiresAt are set for a zap bill.
type committedBill struct {
	App          *db.App
	Token        string
	ZapIssuedAt  uint64
	ZapExpiresAt uint64
}

// commit runs plan in one transaction:
//
//	lock every involved app (postgres advisory locks, ascending; sqlite's
//	IMMEDIATE transaction already holds the one write lock)
//	create each new bill's app and derive its key from the app id
//	sign each bill's token (the node signs inside the open transaction, so the
//	  bill exists for no one until it commits)
//	write the bills' slices, claim the sources or reassign in place
//	move the value as ledger rows, consume every claim proof
//	store each token, write the record, re-check the deadline last
//
// Any failure before COMMIT leaves no app, slice change, transfer, record or
// consumed proof. After COMMIT nothing is undone; only then is each new bill
// announced to the relay layer.
func commit(ctx context.Context, deps Deps, plan commitPlan) (out []committedBill, err error) {
	start := time.Now()
	defer func() {
		ev := logger.Logger.Debug()
		if err != nil {
			ev = logger.Logger.Warn().Err(err)
		}
		ev.Str("op", plan.op).Dur("cash_commit_seconds", time.Since(start)).Bool("committed", err == nil).
			Msg("Cash commit")
	}()

	err = deps.DB.Transaction(func(tx *gorm.DB) error {
		out = nil
		if err := lockAppsTx(tx, plan.lockApps); err != nil {
			return err
		}
		if err := deps.before(StageAfterLock); err != nil {
			return err
		}

		billApps := make([]*db.App, len(plan.bills))
		secrets := make([]string, len(plan.bills))
		for i, b := range plan.bills {
			app, secret, err := createBillAppTx(tx, deps, plan.hub, b)
			if err != nil {
				return err
			}
			billApps[i], secrets[i] = app, secret
		}
		if err := deps.before(StageAfterCreateApp); err != nil {
			return err
		}

		tokens := make([]string, len(plan.bills))
		signed := make([]nipcash.Token, len(plan.bills))
		zaps := make([]*nipcash.ZapContext, len(plan.bills))
		for i, b := range plan.bills {
			identityRequired := b.identityRequired
			if b.zap != nil {
				// issued_at is the clock inside the commit, before the signature.
				zaps[i] = &nipcash.ZapContext{Recipient: b.zap.recipient, Sender: b.zap.sender, ZapTarget: b.zap.target,
					IssuedAt: uint64(deps.now().Unix()), ExpiresAt: b.zap.expiresAt} //nolint:gosec // a unix time
				if err := storeZapFieldsTx(tx, billApps[i].ID, zaps[i]); err != nil {
					return err
				}
			}
			tok, encoded, err := issueCashToken(ctx, deps.LNClient, *billApps[i].WalletPubkey, secrets[i], deps.RelayURLs, &identityRequired, b.amount, plan.hub, zaps[i])
			if err != nil {
				return fmt.Errorf("refusing to issue a bill without a signed token: %w", err)
			}
			signed[i], tokens[i] = tok, encoded
		}
		if err := deps.before(StageAfterSign); err != nil {
			return err
		}

		for i, b := range plan.bills {
			if err := deps.AppsService.CreateCashWalletClaimsTx(tx, billApps[i].ID, b.claims); err != nil {
				return fmt.Errorf("store the bill's slices: %w", err)
			}
		}
		if err := deps.before(StageAfterClaims); err != nil {
			return err
		}

		now := deps.now()
		var spunOffTo *uint
		if len(billApps) > 0 {
			spunOffTo = &billApps[0].ID
		}
		endedAs := db.CashSliceStatusTransferred
		if plan.op == "consolidate" {
			endedAs = db.CashSliceStatusConsolidated
		}
		for i, ref := range plan.sources {
			if err := apps.ClaimCashSliceTx(tx, ref, spunOffTo, endedAs, now); err != nil {
				return err
			}
			if err := deps.before(fmt.Sprintf("%s(%d)", StageAfterSourceClaim, i)); err != nil {
				return err
			}
		}
		if r := plan.reassign; r != nil {
			if err := apps.ReassignCashSliceIdentityTx(tx, r.ref, r.newIdentityType, r.newIdentityValue, r.newIAKey); err != nil {
				return err
			}
			if err := deps.before(StageAfterReassign); err != nil {
				return err
			}
		}

		for i, t := range plan.transfers {
			if err := ledger.InternalTransferTx(tx, t.fromAppID, billApps[t.toBill].ID, t.amount, "cash "+plan.op); err != nil {
				return err
			}
			if err := deps.before(fmt.Sprintf("%s(%d)", StageAfterTransfer, i)); err != nil {
				return err
			}
		}

		for i, p := range plan.proofs {
			if err := deps.requests().ConsumeProofTx(tx, p.EventID, p.CreatedAt, p.Method); err != nil {
				if errors.Is(err, cashrequest.ErrProofConsumed) {
					return ErrProofUsed
				}
				return err
			}
			if err := deps.before(fmt.Sprintf("%s(%d)", StageAfterProof, i)); err != nil {
				return err
			}
		}

		for i := range billApps {
			if err := tx.Model(&db.App{}).Where("id = ?", billApps[i].ID).Update("cash_token", tokens[i]).Error; err != nil {
				return fmt.Errorf("store the bill's token: %w", err)
			}
		}

		if plan.record != nil {
			bills := make([]db.CashRequestRecordBill, len(billApps))
			for i := range billApps {
				bills[i] = recordBill(plan.bills[i].role, billApps[i], signed[i], zaps[i])
			}
			if err := deps.requests().CommitTx(tx, plan.record, bills); err != nil {
				return err
			}
		}
		if err := deps.before(StageAfterRecord); err != nil {
			return err
		}
		if err := deps.before(StageBeforeCommit); err != nil {
			return err
		}
		// The deadline is checked again as the last statement before COMMIT.
		if plan.record != nil && !nipcash.CommitAllowed(plan.record.Deadline, deps.now().Unix()) {
			return cashrequest.ErrDeadlinePassed
		}

		out = make([]committedBill, len(billApps))
		for i := range billApps {
			out[i] = committedBill{App: billApps[i], Token: tokens[i]}
			if zaps[i] != nil {
				out[i].ZapIssuedAt, out[i].ZapExpiresAt = zaps[i].IssuedAt, zaps[i].ExpiresAt
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, b := range out {
		deps.AppsService.NotifyAppCreated(b.App)
	}
	return out, nil
}

// lockAppsTx takes the per-app advisory lock SendPaymentSync uses
// (transactions_service.go) for every involved app, in ascending order so two
// commits, or a commit and a payment, never deadlock. sqlite needs none: its
// IMMEDIATE transaction holds the single write lock from BEGIN.
func lockAppsTx(tx *gorm.DB, appIDs []uint) error {
	if tx.Name() != "postgres" || len(appIDs) == 0 {
		return nil
	}
	ids := append([]uint(nil), appIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var last uint
	for i, id := range ids {
		if i > 0 && id == last {
			continue
		}
		last = id
		if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(id)).Error; err != nil { //nolint:gosec // app ids are small auto-increment keys
			return fmt.Errorf("lock app %d: %w", id, err)
		}
	}
	return nil
}

// createBillAppTx inserts a new bill's app inside tx and registers the pairing
// key derived from its id. It does not announce the app.
func createBillAppTx(tx *gorm.DB, deps Deps, hub *db.App, b newBill) (*db.App, string, error) {
	app, _, err := deps.AppsService.CreateAppTx(tx,
		apps.GenerateChildName(hub.Name, b.nameSeed),
		"", // a temporary random key, replaced just below
		b.amount/1000,
		constants.BUDGET_RENEWAL_NEVER,
		b.expiresAt,
		cashWalletScopes,
		db.AppKindCashWallet,
		&hub.ID,
		db.ParentKindCash,
		nil,
	)
	if err != nil {
		return nil, "", fmt.Errorf("create the bill: %w", err)
	}
	secret, err := deps.Keys.GetCashPairingKey(app.ID)
	if err != nil {
		return nil, "", fmt.Errorf("derive the bill's pairing key: %w", err)
	}
	pubkey, err := nostr.GetPublicKey(secret)
	if err != nil {
		return nil, "", fmt.Errorf("derive the bill's pairing pubkey: %w", err)
	}
	root := app.ID
	if b.lineageRoot != nil {
		root = *b.lineageRoot
	}
	updates := map[string]interface{}{"app_pubkey": pubkey, "cash_lineage_root_id": root}
	if b.splitFrom != nil {
		updates["split_from_wallet_app_id"] = *b.splitFrom
	}
	if err := tx.Model(&db.App{}).Where("id = ?", app.ID).Updates(updates).Error; err != nil {
		return nil, "", fmt.Errorf("register the bill's pairing key: %w", err)
	}
	app.AppPubkey = pubkey
	app.SplitFromWalletAppID = b.splitFrom
	app.CashLineageRootID = &root
	return app, secret, nil
}

// LineageRoot is the lineage root of bill: the minted bill its value descends
// from (its own id when it was minted).
func LineageRoot(bill *db.App) uint {
	if bill.CashLineageRootID != nil {
		return *bill.CashLineageRootID
	}
	return bill.ID
}

// recordBill is what a lookup needs to rebuild a bill's token without storing
// it: the connection secret is re-derived from the app id.
func recordBill(role string, app *db.App, tok nipcash.Token, zap *nipcash.ZapContext) db.CashRequestRecordBill {
	relays, _ := json.Marshal(tok.RelayURLs)
	if tok.RelayURLs == nil {
		relays = []byte("[]")
	}
	rb := db.CashRequestRecordBill{
		Role:         role,
		WalletAppID:  app.ID,
		WalletPubkey: tok.WalletPubkey,
		RelayHints:   string(relays),
		Scheme:       int16(tok.Scheme),
		Signature:    hex.EncodeToString(tok.MintSignature),
	}
	if tok.AttestedAmountMillis != nil {
		rb.AmountMillis = int64(*tok.AttestedAmountMillis) //nolint:gosec // bounded by the hub's int64 ceilings
	}
	if tok.IdentityRequired != nil {
		v := int16(0)
		if *tok.IdentityRequired {
			v = 1
		}
		rb.IdentityRequired = &v
	}
	if len(tok.HubGroup) > 0 {
		g := hex.EncodeToString(tok.HubGroup)
		rb.HubGroup = &g
	}
	if zap != nil {
		f := zapFields(zap)
		rb.ZapIssuedAt, rb.ZapExpiresAt, rb.ZapTargetHash, rb.ZapRecipient, rb.ZapSender =
			f.issuedAt, f.expiresAt, f.targetHash, f.recipient, f.sender
	}
	return rb
}

// storedZapFields are a zap bill's scheme-2 fields as stored: the target only as
// its sha256, the identities as pubkey/<hex>.
type storedZapFields struct {
	issuedAt, expiresAt           *int64
	targetHash, recipient, sender *string
}

func zapFields(z *nipcash.ZapContext) storedZapFields {
	sum := sha256.Sum256([]byte(z.ZapTarget))
	issued, expires := int64(z.IssuedAt), int64(z.ExpiresAt) //nolint:gosec // unix times
	hash := hex.EncodeToString(sum[:])
	recipient, sender := "pubkey/"+z.Recipient, "pubkey/"+z.Sender
	return storedZapFields{issuedAt: &issued, expiresAt: &expires, targetHash: &hash, recipient: &recipient, sender: &sender}
}

// storeZapFieldsTx keeps a zap bill's scheme-2 fields on its app for the bill's
// life (NIP-CASH §Mint Provenance).
func storeZapFieldsTx(tx *gorm.DB, appID uint, z *nipcash.ZapContext) error {
	f := zapFields(z)
	return tx.Model(&db.App{}).Where("id = ?", appID).Updates(map[string]interface{}{
		"zap_issued_at": f.issuedAt, "zap_expires_at": f.expiresAt, "zap_target_hash": f.targetHash,
		"zap_recipient": f.recipient, "zap_sender": f.sender,
	}).Error
}
