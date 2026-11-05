package decodepay

import (
	"strings"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnwire"
	"github.com/flokiorg/flnd/zpay32"
	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/ecdsa"
	"github.com/stretchr/testify/require"
)

func signedInvoice(t *testing.T, net *chaincfg.Params, amount lnwire.MilliLoki) string {
	t.Helper()
	priv, _ := crypto.PrivKeyFromBytes([]byte(strings.Repeat("\x07", 32)))
	var hash [32]byte
	hash[0] = 1
	opts := []func(*zpay32.Invoice){zpay32.Description("regtest")}
	if amount > 0 {
		opts = append(opts, zpay32.Amount(amount))
	}
	inv, err := zpay32.NewInvoice(net, hash, time.Unix(1_700_000_000, 0), opts...)
	require.NoError(t, err)
	s, err := inv.Encode(zpay32.MessageSigner{SignCompact: func(msg []byte) ([]byte, error) {
		return ecdsa.SignCompact(priv, chainhash.HashB(msg), true), nil
	}})
	require.NoError(t, err)
	return s
}

// A regtest invoice (lnfcrt...) decodes, and its network is read from the HRP.
func TestDecode_NetworkFromHRP(t *testing.T) {
	cases := []struct {
		net  *chaincfg.Params
		want string
	}{
		{&chaincfg.MainNetParams, "flokicoin"},
		{&chaincfg.RegressionNetParams, "regtest"},
		{&chaincfg.TestNet3Params, "testnet"},
	}
	for _, tc := range cases {
		for _, amount := range []lnwire.MilliLoki{0, 21_000} {
			bolt11 := signedInvoice(t, tc.net, amount)
			got, err := Decode(bolt11)
			require.NoError(t, err, bolt11)
			require.Equal(t, tc.want, got.Network, bolt11)
			require.Equal(t, int64(amount), got.AmountMloki)
		}
	}
	require.True(t, strings.HasPrefix(signedInvoice(t, &chaincfg.RegressionNetParams, 0), "lnfcrt1"))
}
