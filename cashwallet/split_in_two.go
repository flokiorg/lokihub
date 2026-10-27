package cashwallet

import (
	"context"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// SplitInTwoParams spins a source slice off into one or two fresh dedicated
// bills. The carved bill (for the target identity) is always created; the
// remainder bill (back to the caller's own identity) only when
// RemainderAmountMloki > 0. Both are funded from SourceWalletApp's balance and
// inherit the source slice's terms and expiry.
//
// Source is the slice exactly as the caller read and authorized it; the commit
// claims it terminal, so a slice that changed since fails the call with
// apps.ErrSliceClaimed and nothing is written.
type SplitInTwoParams struct {
	HubApp          *db.App
	SourceWalletApp *db.App
	Source          apps.SliceRef

	CarvedIdentityType  string
	CarvedIdentityValue string
	CarvedIAPubkey      string
	CarvedAmountMloki   uint64

	// Remainder* describe the bill the caller keeps their change in.
	// RemainderAmountMloki == 0 means a full spin-off (no remainder bill).
	RemainderIdentityType  string
	RemainderIdentityValue string
	RemainderIAPubkey      string
	RemainderAmountMloki   uint64

	MinTransferMloki int64
	RedeemFeePpm     int
	// RedeemFeeBaseMloki travels with RedeemFeePpm: they are one quoted price.
	RedeemFeeBaseMloki int64
	// Ordered: a wallet made from an ordered slice is ordered (NIP-CASH §Data
	// Model).
	Ordered   bool
	ExpiresAt *time.Time

	// Proofs are consumed by the commit, and only if it commits.
	Proofs []ClaimProof
	// Record, when set, is the call's record, written last under its deadline.
	Record *db.CashRequestRecord

	// Zap, when set, makes this a zap transfer (NIP-CZ): the carved bill is
	// signed under the zap scheme for its pubkey, the remainder under the ordinary
	// one.
	Zap *ZapParams
}

// ZapParams are a zap transfer's scheme-2 inputs besides the carved bill: the
// zapped event (signed as its sha256, never stored) and the source slice's
// pubkey, whose proof authorized the transfer.
type ZapParams struct {
	Target string
	Sender string
}

// SplitInTwoResult carries the created bill(s). Remainder is nil for a full
// spin-off.
type SplitInTwoResult struct {
	Carved    *SplitResult
	Remainder *SplitResult
}

// SplitInTwo performs the one-to-two spin-off as one commit: the source claim,
// both bills, their signed tokens, the funding rows, the consumed proofs and the
// record commit together or not at all. On any error the source slice is
// untouched and claimable, and the proofs stay unused.
func SplitInTwo(ctx context.Context, deps Deps, params SplitInTwoParams) (*SplitInTwoResult, error) {
	source := params.SourceWalletApp.ID
	root := LineageRoot(params.SourceWalletApp)
	bill := func(role, identityType, identityValue, iaPubkey string, amount uint64) newBill {
		return newBill{
			role:      role,
			nameSeed:  identityValue,
			amount:    amount,
			expiresAt: params.ExpiresAt,
			claims: []db.CashWalletClaim{{
				IdentityType:       identityType,
				IdentityValue:      identityValue,
				IAPubkey:           iaPubkey,
				AmountMloki:        int64(amount), //nolint:gosec // bounded by the source slice's own int64 amount
				MinTransferMloki:   params.MinTransferMloki,
				RedeemFeePpm:       params.RedeemFeePpm,
				RedeemFeeBaseMloki: params.RedeemFeeBaseMloki,
				Ordered:            params.Ordered,
			}},
			identityRequired: identityType != db.CashIdentityCash,
			splitFrom:        &source,
			lineageRoot:      &root,
		}
	}

	carvedRole := db.CashRecordBillSingle
	if params.RemainderAmountMloki > 0 {
		carvedRole = db.CashRecordBillCarved
	}
	carved := bill(carvedRole, params.CarvedIdentityType, params.CarvedIdentityValue, params.CarvedIAPubkey, params.CarvedAmountMloki)
	if params.Zap != nil {
		// The zap bill inherits the source's expiry, or 0 for never.
		var expires uint64
		if params.ExpiresAt != nil {
			expires = uint64(params.ExpiresAt.Unix()) //nolint:gosec // a unix time
		}
		carved.zap = &zapBill{recipient: params.CarvedIdentityValue, sender: params.Zap.Sender,
			target: params.Zap.Target, expiresAt: expires}
	}
	plan := commitPlan{
		op:        "split",
		hub:       params.HubApp,
		lockApps:  []uint{source},
		bills:     []newBill{carved},
		sources:   []apps.SliceRef{params.Source},
		transfers: []internalTransfer{{fromAppID: source, toBill: 0, amount: params.CarvedAmountMloki}},
		proofs:    params.Proofs,
		record:    params.Record,
	}
	if params.RemainderAmountMloki > 0 {
		plan.bills = append(plan.bills, bill(db.CashRecordBillRemainder, params.RemainderIdentityType,
			params.RemainderIdentityValue, params.RemainderIAPubkey, params.RemainderAmountMloki))
		plan.transfers = append(plan.transfers, internalTransfer{fromAppID: source, toBill: 1, amount: params.RemainderAmountMloki})
	}

	out, err := commit(ctx, deps, plan)
	if err != nil {
		return nil, err
	}

	result := &SplitInTwoResult{Carved: &SplitResult{WalletApp: out[0].App, CashToken: out[0].Token}}
	if params.Zap != nil {
		result.Carved.Zap = &ZapTimes{IssuedAt: out[0].ZapIssuedAt, ExpiresAt: out[0].ZapExpiresAt}
	}
	if len(out) > 1 {
		result.Remainder = &SplitResult{WalletApp: out[1].App, CashToken: out[1].Token}
	}
	logger.Logger.Info().
		Uint("source_wallet_id", source).
		Uint("carved_wallet_id", out[0].App.ID).
		Uint64("carved_amount_mloki", params.CarvedAmountMloki).
		Uint64("remainder_amount_mloki", params.RemainderAmountMloki).
		Msg("Slice split off into fresh dedicated Cash wallet(s)")
	return result, nil
}

// ReassignParams moves a whole slice to a new identity in place, on its own bill.
type ReassignParams struct {
	WalletApp        *db.App
	Source           apps.SliceRef
	NewIdentityType  string
	NewIdentityValue string
	NewIAPubkey      string
	Proofs           []ClaimProof
	Record           *db.CashRequestRecord
}

// Reassign is the in-place transfer as one commit: the identity change, the
// consumed proofs and the record commit together or not at all.
func Reassign(ctx context.Context, deps Deps, params ReassignParams) error {
	_, err := commit(ctx, deps, commitPlan{
		op:       "transfer",
		lockApps: []uint{params.WalletApp.ID},
		reassign: &reassignment{
			ref:              params.Source,
			newIdentityType:  params.NewIdentityType,
			newIdentityValue: params.NewIdentityValue,
			newIAKey:         params.NewIAPubkey,
		},
		proofs: params.Proofs,
		record: params.Record,
	})
	return err
}
