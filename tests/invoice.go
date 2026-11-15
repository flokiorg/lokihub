package tests

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/flokiorg/flnd/lnwire"
	"github.com/flokiorg/flnd/zpay32"
	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/ecdsa"
)

// SignedInvoice returns a real, signed regtest BOLT11 for amountMloki, stamped
// at created and expiring expiry later, and its payment hash. Use it where the
// code under test decodes the invoice a node returned.
func SignedInvoice(amountMloki int64, created time.Time, expiry time.Duration) (invoice string, paymentHash string) {
	preimage := make([]byte, 32)
	if _, err := rand.Read(preimage); err != nil {
		panic(err)
	}
	hash := sha256.Sum256(preimage)
	key, err := crypto.NewPrivateKey()
	if err != nil {
		panic(err)
	}
	inv, err := zpay32.NewInvoice(&chaincfg.RegressionNetParams, hash, created,
		zpay32.Amount(lnwire.MilliLoki(amountMloki)), //nolint:gosec // test amounts are positive
		zpay32.Description("test"),
		zpay32.Expiry(expiry))
	if err != nil {
		panic(err)
	}
	encoded, err := inv.Encode(zpay32.MessageSigner{SignCompact: func(msg []byte) ([]byte, error) {
		return ecdsa.SignCompact(key, chainhash.HashB(msg), true), nil
	}})
	if err != nil {
		panic(err)
	}
	return encoded, hex.EncodeToString(hash[:])
}
