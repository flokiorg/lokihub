package regtest

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flokiorg/go-flokicoin/chainutil"
)

// WriteChainCerts writes the chain node's RPC certificate and key into dir (its
// data directory), valid for hosts, unless both already exist. The chain node uses
// them instead of generating its own, which would only name its container hostname
// and localhost, so a tool on another host could not verify it.
func WriteChainCerts(dir string, hosts []string) error {
	certPath, keyPath := filepath.Join(dir, "rpc.cert"), filepath.Join(dir, "rpc.key")
	if exists(certPath) && exists(keyPath) {
		return nil
	}
	cert, key, err := chainutil.NewTLSCertPair("lokihub regtest dev stack", time.Now().AddDate(10, 0, 0), hosts)
	if err != nil {
		return fmt.Errorf("generate chain rpc cert: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, cert, 0o644); err != nil {
		return err
	}
	return os.WriteFile(keyPath, key, 0o600)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
