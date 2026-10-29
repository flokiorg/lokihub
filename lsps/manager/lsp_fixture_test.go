package manager

import (
	"encoding/json"
	"strings"

	"github.com/ohstr/nmilat/niplsp"

	"github.com/flokiorg/lokihub/lsps/persist"
)

// lenientFees announce LSPS1, LSPS2 and LSPS5 with a fee row no test menu is
// worse than, for tests about something other than the terms.
var lenientFees = []niplsp.FeeRow{{MinFeeMillis: 1 << 40, Proportional: 1 << 31, MinPaymentSizeMillis: 1 << 40}}

// announce stores fees as the row's announced LSPS2 terms, as discovery does.
func announce(l *persist.LSP, fees ...niplsp.FeeRow) {
	p, _ := json.Marshal([]int{1, 2, 5})
	f, _ := json.Marshal(fees)
	l.Protocols, l.LSPS2Fees = string(p), string(f)
}

// addTestLSP puts an LSP in the catalogue as discovery would after a usable
// open announcement, with the node id already resolved.
func (m *LSPManager) addTestLSP(name, nodeID, host string, trusted bool) (*persist.LSP, error) {
	nodeID = strings.ToLower(nodeID)
	lsp := persist.LSP{NodeKey: NodeKeyOf(nodeID), Pubkey: nodeID, Host: host, Name: name,
		Trusted: trusted, Usable: true, Status: "open"}
	announce(&lsp, lenientFees...)
	return &lsp, m.db.Create(&lsp).Error
}

// addTestLSP adds pubkey@host, trusted when it is the first (as the removed
// AddLSP did).
func (m *LiquidityManager) addTestLSP(name, uri string) error {
	pubkey, host, _ := strings.Cut(uri, "@")
	all, err := m.cfg.LSPManager.ListLSPs()
	if err != nil {
		return err
	}
	_, err = m.cfg.LSPManager.addTestLSP(name, pubkey, host, len(all) == 0)
	return err
}
