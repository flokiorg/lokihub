package cashwallet

import (
	"context"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// ConsolidateSource is one input slice to a consolidation: the source bill this
// node custodies, the slice exactly as the caller read and authorized it, and
// its committed amount.
type ConsolidateSource struct {
	WalletApp   *db.App
	Slice       apps.SliceRef
	AmountMloki uint64
}

// ConsolidateParams describes merging several source slices into one new
// dedicated bill for NewIdentity. The caller has verified that this node
// custodies every source, that they share HubApp, that the caller controls each,
// that the total fits the Hub's ceiling and that the sources agree on their
// terms. The commit claims every source terminal itself.
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
	// RedeemFeeBaseMloki travels with RedeemFeePpm — they are one quoted price.
	RedeemFeeBaseMloki int64
	// Ordered: a merge of ordered sources is ordered.
	Ordered bool
	// UnfinalPaymentIDs: the union of the sources' (db.MergePaymentIDs).
	UnfinalPaymentIDs *string
	ExpiresAt         *time.Time

	// Proofs are consumed by the commit, and only if it commits.
	Proofs []ClaimProof
	// Record, when set, is the call's record, written last under its deadline.
	Record *db.CashRequestRecord
	// LiveBillCap, when set, bounds the Hub's live bills inside the commit.
	LiveBillCap *LiveBillCap
}

// ConsolidateResult carries the merged wallet and its token, delivered to the
// caller the same nested-encrypted way a split-off wallet's is.
type ConsolidateResult struct {
	WalletApp   *db.App
	CashToken   string
	AmountMloki uint64
}

// Consolidate merges the sources into one new bill as one commit: every source
// claim, the bill, its signed token, one funding row pair per source, the
// consumed proofs and the record commit together or not at all. On any error
// every source is untouched and claimable.
func Consolidate(ctx context.Context, deps Deps, params ConsolidateParams) (*ConsolidateResult, error) {
	if len(params.Sources) < 2 {
		return nil, fmt.Errorf("%w: consolidate needs at least two sources", constants.ErrInvalidParams)
	}
	var total uint64
	lock := make([]uint, 0, len(params.Sources))
	refs := make([]apps.SliceRef, 0, len(params.Sources))
	transfers := make([]internalTransfer, 0, len(params.Sources))
	for _, s := range params.Sources {
		total += s.AmountMloki // overflow already guarded by the caller
		lock = append(lock, s.WalletApp.ID)
		refs = append(refs, s.Slice)
		transfers = append(transfers, internalTransfer{fromAppID: s.WalletApp.ID, toBill: 0, amount: s.AmountMloki})
	}

	// A merge continues its first source's lineage.
	root := LineageRoot(params.Sources[0].WalletApp)
	out, err := commit(ctx, deps, commitPlan{
		op:       "consolidate",
		hub:      params.HubApp,
		lockApps: lock,
		bills: []newBill{{
			role:      db.CashRecordBillMerged,
			nameSeed:  params.NewIdentityValue,
			amount:    total,
			expiresAt: params.ExpiresAt,
			claims: []db.CashWalletClaim{{
				IdentityType:       params.NewIdentityType,
				IdentityValue:      params.NewIdentityValue,
				IAPubkey:           params.NewIAPubkey,
				AmountMloki:        int64(total), //nolint:gosec // bounded by the hub's int64 per-wallet ceiling
				MinTransferMloki:   params.MinTransferMloki,
				RedeemFeePpm:       params.RedeemFeePpm,
				RedeemFeeBaseMloki: params.RedeemFeeBaseMloki,
				Ordered:            params.Ordered,
				UnfinalPaymentIDs:  params.UnfinalPaymentIDs,
			}},
			identityRequired: params.NewIdentityType != db.CashIdentityCash,
			lineageRoot:      &root,
		}},
		sources:     refs,
		transfers:   transfers,
		proofs:      params.Proofs,
		record:      params.Record,
		liveBillCap: params.LiveBillCap,
	})
	if err != nil {
		return nil, err
	}

	logger.Logger.Info().
		Uint("cash_wallet_id", out[0].App.ID).
		Uint("parent_app_id", params.HubApp.ID).
		Int("source_count", len(params.Sources)).
		Uint64("total_mloki", total).
		Msg("Slices consolidated into a new dedicated Cash wallet")

	return &ConsolidateResult{
		WalletApp:   out[0].App,
		CashToken:   out[0].Token,
		AmountMloki: total,
	}, nil
}
