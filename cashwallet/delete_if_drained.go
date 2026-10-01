package cashwallet

import (
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/logger"
)

// deleteIfDrained deletes app only once the LEDGER says it holds nothing.
//
// Both compensating sagas — Consolidate's merged wallet and SplitInTwo's carved one —
// used to delete on the strength of their reversal call returning nil. That is a
// weaker statement than "the wallet is empty": a reversal that reports success while
// moving nothing, or less than it claimed, leaves funds behind, and DeleteCashBill
// then archives them away leaving no record of where they went. The source claim is
// also reported safe to restore in that case, so the same value can be handed back to
// its recipient while still sitting in the archived wallet.
//
// Session B's B-3 described this shape. Its trigger was never confirmed as
// holder-inducible, and this guard is cheap enough not to need one — the point is that
// the delete now rests on observed state rather than on inferred control flow.
//
// On a non-zero balance it does exactly what a FAILED reversal already does: leave the
// wallet in place as the only record of where the funds are, and record a
// CashStrandedFund so an operator finds it by query rather than by grepping logs.
// Returning false tells the caller the wallet survived, which is also why the source
// claim must not be restored.
//
// A negative balance is treated as drained. It means more has left than arrived, which
// is an accounting question rather than money sitting here, and refusing to delete on
// it would pin an empty wallet forever — the shape of the invoice-pin leak this round
// already fixed once.
func deleteIfDrained(deps Deps, operation string, sourceWalletAppID uint, app *db.App) bool {
	remaining := queries.GetIsolatedBalance(deps.DB, app.ID)
	if remaining > 0 {
		logger.Logger.Error().
			Str("operation", operation).
			Uint("retained_wallet_id", app.ID).
			Uint("source_wallet_id", sourceWalletAppID).
			Int64("remaining_mloki", remaining).
			Msg("Every reversal reported success but the wallet still holds funds — NOT deleting it, since archiving it would erase the only record of where they are; manual sweep recommended, source claim must not be restored")
		if recErr := deps.AppsService.RecordCashStrandedFund(operation, sourceWalletAppID, app.ID, uint64(remaining)); recErr != nil { //nolint:gosec // remaining > 0 on this branch
			logger.Logger.Error().Err(recErr).
				Uint("retained_wallet_id", app.ID).
				Msg("Failed to durably record a stranded-fund reconciliation entry; the log line above is the only record")
		}
		return false
	}
	if derr := deps.AppsService.DeleteCashBill(app, db.CashBillOutcomeVoid); derr != nil {
		logger.Logger.Error().Err(derr).Uint("wallet_id", app.ID).
			Msg("Reversed everything but failed to delete the emptied wallet; harmless but leaves a zero-balance app")
	}
	return true
}
