package manager

import (
	"strings"

	"github.com/flokiorg/lokihub/lsps/persist"
)

// addTestLSP puts an LSP in the catalogue as discovery would after a usable
// open announcement, with the node id already resolved.
func (m *LSPManager) addTestLSP(name, nodeID, host string, trusted bool) (*persist.LSP, error) {
	nodeID = strings.ToLower(nodeID)
	lsp := persist.LSP{NodeKey: NodeKeyOf(nodeID), Pubkey: nodeID, Host: host, Name: name,
		Trusted: trusted, Usable: true, Status: "open"}
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
