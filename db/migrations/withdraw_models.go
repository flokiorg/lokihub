package migrations

import "github.com/flokiorg/lokihub/db"

// withdrawModels are the on-chain withdrawal tables (WP-23, WP-24), kept in their
// own file so they rebase onto other schema changes without touching the main
// list. The schema is created fresh (no conversion), so there is no migration
// number.
func withdrawModels() []any {
	return []any{
		&db.CashWithdrawTerms{},
		&db.CashWithdrawal{},
		&db.CashWithdrawEvent{},
		&db.CashWithdrawBatch{},
		&db.CashWithdrawVersion{},
		&db.CashWithdrawVersionOutput{},
		&db.CashWithdrawCoin{},
		&db.CashWithdrawSetAside{},
	}
}
