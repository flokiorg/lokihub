// Package regtest drives the regtest dev and test stack: the chain node (lokid),
// the Hub's flnd, the payer flnd and lokihub's first-run setup. It refuses any node
// that is not on regtest, so it can never act on a mainnet chain or node.
package regtest

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/chainjson"
	"github.com/flokiorg/go-flokicoin/chainutil"
	"github.com/flokiorg/go-flokicoin/integration/rpctest"
	"github.com/flokiorg/go-flokicoin/rpcclient"
	"github.com/flokiorg/go-flokicoin/wire"
)

// Params are the chain params of the stack.
var Params = &chaincfg.RegressionNetParams

// ErrNotRegtest is returned for a chain or node on any other network.
var ErrNotRegtest = errors.New("not a regtest node")

// ChainConfig names the chain node's RPC endpoint.
type ChainConfig struct {
	Host     string // host:port
	User     string
	Pass     string
	CertPath string // the chain node's rpc.cert
}

// Chain is the chain node (lokid --regtest).
type Chain struct {
	rpc *rpcclient.Client
}

// DialChain connects to the chain node and refuses one whose chain is not regtest.
func DialChain(cfg ChainConfig) (*Chain, error) {
	cert, err := os.ReadFile(cfg.CertPath)
	if err != nil {
		return nil, fmt.Errorf("read chain rpc cert: %w", err)
	}
	rpc, err := rpcclient.New(&rpcclient.ConnConfig{
		Host:         cfg.Host,
		User:         cfg.User,
		Pass:         cfg.Pass,
		Certificates: cert,
		HTTPPostMode: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect chain rpc: %w", err)
	}
	c := &Chain{rpc: rpc}
	info, err := rpc.GetBlockChainInfo()
	if err != nil {
		rpc.Shutdown()
		return nil, fmt.Errorf("getblockchaininfo: %w", err)
	}
	if err := CheckChainName(info.Chain); err != nil {
		rpc.Shutdown()
		return nil, err
	}
	return c, nil
}

// CheckChainName refuses a chain node whose getblockchaininfo chain is not regtest.
func CheckChainName(chain string) error {
	if chain != Params.Name {
		return fmt.Errorf("%w: chain node reports chain %q", ErrNotRegtest, chain)
	}
	return nil
}

// Close disconnects.
func (c *Chain) Close() { c.rpc.Shutdown() }

// Tip is the best block's height.
func (c *Chain) Tip() (int32, error) {
	_, height, err := c.rpc.GetBestBlock()
	return height, err
}

// Mine builds n blocks paying their coinbase to address, with every mempool
// transaction except those in skip (and their descendants), and submits them.
// Blocks are built here rather than by the node, so the coinbase can pay any
// address and a transaction can be left out.
func (c *Chain) Mine(n int, address string, skip map[chainhash.Hash]bool) ([]*chainhash.Hash, error) {
	addr, err := chainutil.DecodeAddress(address, Params)
	if err != nil {
		return nil, fmt.Errorf("coinbase address %q: %w", address, err)
	}
	hashes := make([]*chainhash.Hash, 0, n)
	for i := 0; i < n; i++ {
		bestHash, height, err := c.rpc.GetBestBlock()
		if err != nil {
			return hashes, err
		}
		prevMsg, err := c.rpc.GetBlock(bestHash)
		if err != nil {
			return hashes, err
		}
		prev := chainutil.NewBlock(prevMsg)
		prev.SetHeight(height)

		txs, err := c.mempoolTxs(skip)
		if err != nil {
			return hashes, err
		}
		version, err := c.blockVersion(height + 1)
		if err != nil {
			return hashes, err
		}
		block, err := rpctest.CreateBlock(prev, txs, version, BlockTime(prevMsg.Header.Timestamp, time.Now()), addr, nil, Params)
		if err != nil {
			return hashes, fmt.Errorf("build block %d: %w", height+1, err)
		}
		if err := c.rpc.SubmitBlock(block, nil); err != nil {
			return hashes, fmt.Errorf("submit block %d: %w", height+1, err)
		}
		hashes = append(hashes, block.Hash())
	}
	return hashes, nil
}

// Reorg reorganises the last depth blocks away: the block depth − 1 below the
// tip is invalidated (its transactions and its descendants' go back to the
// node's mempool) and depth + 1 new blocks are mined on its parent to address,
// leaving out skip, so the new branch is the longer one.
func (c *Chain) Reorg(depth int, address string, skip map[chainhash.Hash]bool) ([]*chainhash.Hash, error) {
	if depth < 1 || depth > 100 {
		return nil, errors.New("depth must be between 1 and 100")
	}
	_, height, err := c.rpc.GetBestBlock()
	if err != nil {
		return nil, err
	}
	target := int64(height) - int64(depth) + 1
	if target < 1 {
		return nil, fmt.Errorf("the chain is only %d blocks high", height)
	}
	hash, err := c.rpc.GetBlockHash(target)
	if err != nil {
		return nil, err
	}
	if err := c.rpc.InvalidateBlock(hash); err != nil {
		return nil, fmt.Errorf("invalidateblock %s: %w", hash, err)
	}
	return c.Mine(depth+1, address, skip)
}

// blockVersion is the version the node itself would give the next block: its
// template's version bits (so soft forks such as segwit activate) and, from the
// auxpow height on, the chain ID.
func (c *Chain) blockVersion(height int32) (int32, error) {
	tmpl, err := c.rpc.GetBlockTemplate(&chainjson.TemplateRequest{
		Mode:         "template",
		Capabilities: []string{"coinbasevalue"},
		Rules:        []string{"segwit"},
	})
	if err != nil {
		return 0, fmt.Errorf("getblocktemplate: %w", err)
	}
	return BlockVersion(tmpl.Version, height), nil
}

// BlockVersion stamps the chain ID on a template version from the auxpow height.
func BlockVersion(templateVersion, height int32) int32 {
	h := wire.BlockHeader{Version: templateVersion}
	if height >= Params.AuxpowHeightEffective {
		h.SetChainID(Params.AuxpowChainId)
	}
	return h.Version
}

// BlockTime is a new block's timestamp: now, or one second after the previous
// block when that is later, so a burst of blocks keeps strictly rising times and
// the node stays synced (flnd wants a tip under two hours old).
func BlockTime(prev, now time.Time) time.Time {
	now = now.Truncate(time.Second)
	if now.After(prev) {
		return now
	}
	return prev.Add(time.Second)
}

// mempoolTxs returns the mempool's transactions, parents before children, without
// skip and anything that spends from a skipped transaction.
func (c *Chain) mempoolTxs(skip map[chainhash.Hash]bool) ([]*chainutil.Tx, error) {
	verbose, err := c.rpc.GetRawMempoolVerbose()
	if err != nil {
		return nil, err
	}
	deps := make(map[string][]string, len(verbose))
	for id, entry := range verbose {
		deps[id] = entry.Depends
	}
	skipIDs := make(map[string]bool, len(skip))
	for h := range skip {
		skipIDs[h.String()] = true
	}
	order := OrderMempool(deps, skipIDs)
	txs := make([]*chainutil.Tx, 0, len(order))
	for _, id := range order {
		h, err := chainhash.NewHashFromStr(id)
		if err != nil {
			return nil, err
		}
		tx, err := c.rpc.GetRawTransaction(h)
		if err != nil {
			return nil, err
		}
		txs = append(txs, tx)
	}
	return txs, nil
}

// OrderMempool orders mempool txids so every parent comes before its children,
// dropping each skipped txid and everything that depends on one. deps maps a txid
// to the in-mempool txids it spends from. The order is deterministic.
func OrderMempool(deps map[string][]string, skip map[string]bool) []string {
	ids := make([]string, 0, len(deps))
	for id := range deps {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	state := make(map[string]int, len(ids)) // 0 new, 1 visiting, 2 kept, 3 dropped
	var out []string
	var visit func(id string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 1, 3:
			return false
		case 2:
			return true
		}
		if skip[id] {
			state[id] = 3
			return false
		}
		state[id] = 1
		parents := append([]string(nil), deps[id]...)
		sort.Strings(parents)
		for _, p := range parents {
			if _, inPool := deps[p]; !inPool {
				continue
			}
			if !visit(p) {
				state[id] = 3
				return false
			}
		}
		state[id] = 2
		out = append(out, id)
		return true
	}
	for _, id := range ids {
		visit(id)
	}
	return out
}
