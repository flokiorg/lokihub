package cashwallet

import (
	"context"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/nbd-wtf/go-nostr"
)

// fundInternal moves amountMloki from fromAppID into a freshly-created invoice
// on toAppID over the internal-transfer path — no real Lightning hop, and
// exempt (via internal_transfer metadata, stripped at every external NWC entry
// point) from both the cash full-drain guard and the pay_invoice-scope
// requirement a cash_wallet's real connection deliberately never has. This is
// the one funding primitive every internal transfer in this package reduces
// to: Commit's Hub→Wallet transfer, Split's Source→New transfer, Consolidate's
// N source→merged transfers and their compensating reversals, and
// SplitInTwo's own carved-wallet reversal. Deps.FundInternalOverride, when
// set, replaces the body below entirely — see its own doc comment.
func fundInternal(ctx context.Context, deps Deps, fromAppID, toAppID uint, amountMloki uint64, memo string) error {
	if deps.FundInternalOverride != nil {
		return deps.FundInternalOverride(ctx, fromAppID, toAppID, amountMloki, memo)
	}
	invoice, err := deps.TransactionsService.MakeInvoice(
		ctx, amountMloki, memo, "", 0,
		nil, deps.LNClient, &toAppID, nil, nil, nil, nil, nil, nil,
		&transactions.InternalMakeInvoiceMeta{InternalTransfer: true},
	)
	if err != nil {
		return fmt.Errorf("failed to create internal transfer invoice: %w", err)
	}
	if _, err = deps.TransactionsService.SendPaymentSync(
		invoice.PaymentRequest, nil,
		map[string]interface{}{"internal_transfer": true},
		deps.LNClient, &fromAppID, nil,
	); err != nil {
		return fmt.Errorf("failed to fund via internal transfer: %w", err)
	}
	return nil
}

// ConsolidateSource is one input slice to a consolidation: the source
// cash_wallet this node custodies, and the full committed amount the caller has
// already claimed terminal on it (mirroring Split's "caller claims first"
// contract).
type ConsolidateSource struct {
	WalletApp   *db.App
	AmountMloki uint64
}

// ConsolidateParams describes combining several already-claimed source slices
// into one new dedicated cash_wallet for NewIdentity. The caller (controller)
// is responsible for: verifying this node custodies every source, that they
// share HubApp, that the caller controls each, that the summed amount fits the
// hub ceiling, and that MinTransferMloki/RedeemFeePpm agree across sources —
// then claiming every source slice terminal (ClaimCashSlice) before calling
// here, and on error unclaiming every source EXCEPT those named in the
// returned strandedSourceAppIDs (see Consolidate's doc comment). Consolidate
// itself only creates and funds the merged wallet.
type ConsolidateParams struct {
	HubApp           *db.App
	Sources          []ConsolidateSource
	NewIdentityType  string
	NewIdentityValue string
	NewIAPubkey      string
	// MinTransferMloki/RedeemFeePpm are the agreed values inherited onto the
	// merged slice (the caller has verified every source agrees). ExpiresAt is
	// the earliest among the sources — a consolidate never extends an
	// entitlement (NIP-CASH §Consolidating Tokens).
	MinTransferMloki int64
	RedeemFeePpm     int
	ExpiresAt        *time.Time
	SignMint         bool
}

// ConsolidateResult carries the merged wallet and its token, delivered to the
// caller the same nested-encrypted way a split-off wallet's is.
type ConsolidateResult struct {
	WalletApp   *db.App
	CashToken   string
	AmountMloki uint64
}

// Consolidate creates one spend-only cash_wallet child of params.HubApp for
// NewIdentity and funds it via one internal transfer out of EACH source
// wallet's balance — the many-to-one inverse of Split.
//
// Atomicity is a compensating saga: the new wallet and its claim are reversible
// by deletion, and each completed source→new transfer is reversed (new→source)
// if a LATER source transfer fails, before the wallet is deleted — so a
// mid-loop failure leaves no source drained and no wallet created. A
// compensation that itself fails is logged for manual sweep (the same posture
// Split takes for its own irreversible funding step), and a process crash
// mid-loop is caught by the background cash sweep, exactly as it is for Split.
//
// The caller has already claimed every source slice terminal. On error, it
// MUST NOT blindly unclaim every source: strandedSourceAppIDs names exactly
// those whose compensating reverse-transfer itself failed — for those, the
// funds are still sitting in the (deliberately retained) merged wallet, so
// restoring the claim to its full original amount would create an
// over-entitlement the source's real balance can't back. Every OTHER source —
// never funded, or successfully reversed — is safe to unclaim as before
// (independent security audit, Auditor B, finding 1 —
// data/docs/audits/cash-consolidate-2026-08-29/).
func Consolidate(ctx context.Context, deps Deps, params ConsolidateParams) (result *ConsolidateResult, strandedSourceAppIDs []uint, err error) {
	if len(params.Sources) < 2 {
		return nil, nil, fmt.Errorf("%w: consolidate needs at least two sources", constants.ErrInvalidParams)
	}
	var total uint64
	for _, s := range params.Sources {
		total += s.AmountMloki // overflow already guarded by the caller
	}

	newApp, _, err := deps.AppsService.CreateApp(
		apps.GenerateChildName(params.HubApp.Name, params.NewIdentityValue),
		"", // temporary random keypair; overridden with the deterministic one below
		total/1000,
		constants.BUDGET_RENEWAL_NEVER,
		params.ExpiresAt,
		cashWalletScopes,
		db.AppKindCashWallet,
		&params.HubApp.ID,
		db.ParentKindCash,
		nil,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create consolidated Cash wallet app: %w", err)
	}

	// funded tracks source→new transfers that completed, so a later failure can
	// compensate (move them back) before the new wallet is deleted. Reversal
	// MUST happen while newApp still exists, so it precedes the delete.
	funded := make([]ConsolidateSource, 0, len(params.Sources))
	fullyFunded := false
	defer func() {
		if fullyFunded {
			return
		}
		// Only delete newApp once every completed transfer has been reversed —
		// mirrors SplitInTwo's carved-wallet compensation. If any reversal fails,
		// newApp still holds that source's funds, so deleting it would strand
		// them silently; leave the (now orphaned but non-empty) wallet app in
		// place for a manual sweep instead of erasing the record of where the
		// funds are. Each such source's app ID is reported via
		// strandedSourceAppIDs so the caller knows not to unclaim it.
		allReversed := true
		for _, s := range funded {
			if rerr := fundInternal(ctx, deps, newApp.ID, s.WalletApp.ID, s.AmountMloki, "cash consolidate rollback"); rerr != nil {
				allReversed = false
				strandedSourceAppIDs = append(strandedSourceAppIDs, s.WalletApp.ID)
				logger.Logger.Error().Err(rerr).
					Uint("new_wallet_id", newApp.ID).
					Uint("source_wallet_id", s.WalletApp.ID).
					Uint64("amount_mloki", s.AmountMloki).
					Msg("Failed to reverse a consolidation transfer after a later source failed — funds are stranded in the consolidated wallet; manual sweep recommended, wallet app left in place, source claim must not be restored")
				if recErr := deps.AppsService.RecordCashStrandedFund("consolidate", s.WalletApp.ID, newApp.ID, s.AmountMloki); recErr != nil {
					logger.Logger.Error().Err(recErr).
						Uint("new_wallet_id", newApp.ID).
						Uint("source_wallet_id", s.WalletApp.ID).
						Msg("Failed to durably record a stranded-fund reconciliation entry; the log line above is the only record")
				}
			}
		}
		if allReversed {
			_ = deps.AppsService.DeleteApp(newApp)
		}
	}()

	pairingSecretKey, err := deps.Keys.GetCashPairingKey(newApp.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to derive Cash pairing key: %w", err)
	}
	deterministicPubKey, err := nostr.GetPublicKey(pairingSecretKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to derive Cash pairing pubkey: %w", err)
	}
	if err := deps.DB.Model(&db.App{}).Where("id = ?", newApp.ID).
		Update("app_pubkey", deterministicPubKey).Error; err != nil {
		return nil, nil, fmt.Errorf("failed to register pairing key: %w", err)
	}
	newApp.AppPubkey = deterministicPubKey
	walletPubkey := *newApp.WalletPubkey

	if err := deps.AppsService.CreateCashWalletClaims(newApp.ID, []db.CashWalletClaim{{
		IdentityType:     params.NewIdentityType,
		IdentityValue:    params.NewIdentityValue,
		IAPubkey:         params.NewIAPubkey,
		AmountMloki:      int64(total), //nolint:gosec // bounded to <= the hub's PerWalletMaxMloki by the caller, itself an int64
		MinTransferMloki: params.MinTransferMloki,
		RedeemFeePpm:     params.RedeemFeePpm,
	}}); err != nil {
		return nil, nil, fmt.Errorf("failed to store consolidated recipient claim: %w", err)
	}

	// Fund the merged wallet from each source in turn. A failure here returns,
	// and the defer compensates every transfer that already completed.
	for _, s := range params.Sources {
		if err := fundInternal(ctx, deps, s.WalletApp.ID, newApp.ID, s.AmountMloki, "cash consolidate"); err != nil {
			return nil, nil, fmt.Errorf("failed to fund consolidated Cash wallet from source wallet %d: %w", s.WalletApp.ID, err)
		}
		funded = append(funded, s)
	}
	fullyFunded = true

	// Single-slice by construction, so identity-required follows NewIdentity.
	// The provenance (when SignMint) attests the merged total, immutable for the
	// wallet's life.
	identityRequired := params.NewIdentityType != db.CashIdentityBearer
	token := encodeCashToken(ctx, deps.LNClient, walletPubkey, pairingSecretKey, deps.RelayURLs, &identityRequired, params.SignMint, total)

	logger.Logger.Info().
		Uint("cash_wallet_id", newApp.ID).
		Uint("parent_app_id", params.HubApp.ID).
		Int("source_count", len(params.Sources)).
		Uint64("total_mloki", total).
		Msg("Slices consolidated into a new dedicated Cash wallet")

	return &ConsolidateResult{
		WalletApp:   newApp,
		CashToken:   token,
		AmountMloki: total,
	}, nil, nil
}
