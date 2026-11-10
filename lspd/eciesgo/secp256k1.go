// Vendored from github.com/ecies/go/v2 (MIT, see LICENSE) with only the
// pure-Go dcrec/secp256k1 backend, dropping the go-ethereum cgo path.

package eciesgo

import (
	"crypto/elliptic"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func getCurve() elliptic.Curve {
	return secp256k1.S256()
}