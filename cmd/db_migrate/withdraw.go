package main

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// withdrawSteps copy the on-chain withdrawal tables, kept apart so they rebase
// onto other table changes: the terms, the payouts and their archive facts.
// Referenced tables first.
var withdrawSteps = []struct {
	name string
	run  func(from, to *gorm.DB) error
}{
	{"cash_withdraw_terms", migrateTable[db.CashWithdrawTerms]},
	{"cash_withdrawals", migrateTable[db.CashWithdrawal]},
	{"cash_withdraw_events", migrateTable[db.CashWithdrawEvent]},
	{"cash_withdraw_batches", migrateTable[db.CashWithdrawBatch]},
	{"cash_withdraw_versions", migrateTable[db.CashWithdrawVersion]},
	{"cash_withdraw_version_outputs", migrateTable[db.CashWithdrawVersionOutput]},
	{"cash_withdraw_coins", migrateTable[db.CashWithdrawCoin]},
	{"cash_withdraw_set_asides", migrateTable[db.CashWithdrawSetAside]},
}

func init() {
	for _, s := range withdrawSteps {
		expectedTables = append(expectedTables, s.name)
	}
}

// migrateWithdrawTables copies every withdrawal table.
func migrateWithdrawTables(from, to *gorm.DB) error {
	for _, step := range withdrawSteps {
		logger.Logger.Info().Msg("migrating " + step.name + "...")
		if err := step.run(from, to); err != nil {
			return fmt.Errorf("failed to migrate %s: %w", step.name, err)
		}
	}
	return nil
}
