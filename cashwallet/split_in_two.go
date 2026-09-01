package cashwallet

import (
	"context"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// SplitInTwoParams spins a just-claimed source slice off into one or two fresh
// dedicated wallets. The carved piece (for a target identity) is always
// created; the remainder (back to the caller's own identity) is created only
// when RemainderAmountMloki > 0. Both are funded from SourceWalletApp's own
// balance, and both inherit their terms from the source slice.
//
// The caller MUST have already claimed the source slice terminal (ClaimCashSlice)
// before calling, and MUST unclaim it if this returns an error — mirroring
// Split's own "caller claims first" contract, extended to the two-wallet case.
type SplitInTwoParams struct {
	HubApp          *db.App
	SourceWalletApp *db.App

	CarvedIdentityType  string
	CarvedIdentityValue string
	CarvedIAPubkey      string
	CarvedAmountMloki   uint64

	// Remainder* describe the wallet the caller keeps their own change in.
	// RemainderAmountMloki == 0 means a full spin-off (no remainder wallet) —
	// e.g. a full transfer to bearer on a multi-recipient-history wallet.
	RemainderIdentityType  string
	RemainderIdentityValue string
	RemainderIAPubkey      string
	RemainderAmountMloki   uint64

	MinTransferMloki int64
	RedeemFeePpm     int
	ExpiresAt        *time.Time
	// SignMint requests optional mint provenance on BOTH resulting tokens —
	// each wallet signs independently over its own pubkey and its own fixed
	// amount (see SplitParams.SignMint).
	SignMint bool
}

// SplitInTwoResult carries the created wallet(s). Remainder is nil for a full
// spin-off.
type SplitInTwoResult struct {
	Carved    *SplitResult
	Remainder *SplitResult
}

// SplitInTwo performs the one-to-two spin-off with all-or-nothing atomicity: if
// the remainder spin-off fails after the carved one already funded, the carved
// transfer is compensated (funds moved back to the source, wallet deleted) so
// the caller ends with neither wallet and can safely retry after the source
// slice is unclaimed. A compensation that itself fails is logged for manual
// sweep (the same posture Consolidate takes) — the funds remain the node's own,
// never lost, just briefly misfiled.
//
// The returned sourceFundsIntact reports whether it's safe for the caller to
// restore (UnclaimCashSlice) the source slice to its full original amount: true
// whenever nothing actually left the source (the carved spin-off itself failed)
// or the carved transfer was successfully reversed; false only when the reverse
// itself failed, leaving the carved wallet — deliberately NOT deleted in that
// case — still holding funds the source's restored claim would otherwise
// double-count. On success (nil error), sourceFundsIntact is meaningless: the
// source was consumed terminal by design and MUST NOT be restored regardless
// (independent security audit, Auditor B, finding 2 —
// data/docs/audits/cash-consolidate-2026-08-29/).
func SplitInTwo(ctx context.Context, deps Deps, params SplitInTwoParams) (result *SplitInTwoResult, sourceFundsIntact bool, err error) {
	carved, err := Split(ctx, deps, SplitParams{
		HubApp:           params.HubApp,
		SourceWalletApp:  params.SourceWalletApp,
		AmountMloki:      params.CarvedAmountMloki,
		NewIdentityType:  params.CarvedIdentityType,
		NewIdentityValue: params.CarvedIdentityValue,
		NewIAPubkey:      params.CarvedIAPubkey,
		MinTransferMloki: params.MinTransferMloki,
		RedeemFeePpm:     params.RedeemFeePpm,
		ExpiresAt:        params.ExpiresAt,
		SignMint:         params.SignMint,
	})
	if err != nil {
		// Nothing has left the source yet — safe to restore.
		return nil, true, fmt.Errorf("failed to spin off the carved piece: %w", err)
	}

	if params.RemainderAmountMloki == 0 {
		return &SplitInTwoResult{Carved: carved}, false, nil
	}

	remainder, err := Split(ctx, deps, SplitParams{
		HubApp:           params.HubApp,
		SourceWalletApp:  params.SourceWalletApp,
		AmountMloki:      params.RemainderAmountMloki,
		NewIdentityType:  params.RemainderIdentityType,
		NewIdentityValue: params.RemainderIdentityValue,
		NewIAPubkey:      params.RemainderIAPubkey,
		MinTransferMloki: params.MinTransferMloki,
		RedeemFeePpm:     params.RedeemFeePpm,
		ExpiresAt:        params.ExpiresAt,
		SignMint:         params.SignMint,
	})
	if err != nil {
		// Compensate the carved spin-off: move its funds back to the source,
		// then delete the emptied wallet. Reversal must precede deletion so the
		// wallet still exists to pay from. Delete (and report the source safe
		// to restore) only if the reversal is CONFIRMED — never on its failure,
		// which leaves the carved wallet — deliberately retained — the only
		// record of where the funds actually are.
		if rerr := fundInternal(ctx, deps, carved.WalletApp.ID, params.SourceWalletApp.ID, params.CarvedAmountMloki, "cash split rollback"); rerr != nil {
			logger.Logger.Error().Err(rerr).
				Uint("carved_wallet_id", carved.WalletApp.ID).
				Uint("source_wallet_id", params.SourceWalletApp.ID).
				Uint64("amount_mloki", params.CarvedAmountMloki).
				Msg("Failed to reverse the carved spin-off after the remainder failed — funds are stranded in the carved wallet; manual sweep recommended, source claim NOT restored, wallet app left in place")
			if recErr := deps.AppsService.RecordCashStrandedFund("split", params.SourceWalletApp.ID, carved.WalletApp.ID, params.CarvedAmountMloki); recErr != nil {
				logger.Logger.Error().Err(recErr).
					Uint("carved_wallet_id", carved.WalletApp.ID).
					Uint("source_wallet_id", params.SourceWalletApp.ID).
					Msg("Failed to durably record a stranded-fund reconciliation entry; the log line above is the only record")
			}
			return nil, false, fmt.Errorf("failed to spin off the remainder: %w", err)
		}
		if derr := deps.AppsService.DeleteApp(carved.WalletApp); derr != nil {
			logger.Logger.Error().Err(derr).Uint("carved_wallet_id", carved.WalletApp.ID).
				Msg("Reversed the carved spin-off but failed to delete the emptied wallet; harmless but leaves a zero-balance app")
		}
		return nil, true, fmt.Errorf("failed to spin off the remainder: %w", err)
	}

	return &SplitInTwoResult{Carved: carved, Remainder: remainder}, false, nil
}
