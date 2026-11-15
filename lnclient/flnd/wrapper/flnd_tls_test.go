package wrapper

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

func testMacaroonHex(t *testing.T) string {
	t.Helper()
	m, err := macaroon.New([]byte("0123456789abcdef0123456789abcdef"), []byte("test-id"), "lokihub-test", macaroon.V2)
	require.NoError(t, err)
	b, err := m.MarshalBinary()
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func TestIsLoopbackAddress(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:10009", true},
		{"127.0.0.1", true},
		{"localhost:10009", true},
		{"LocalHost:10009", true},
		{"[::1]:10009", true},
		{"::1", true},
		{"flnd:10005", false},
		{"10.0.0.5:10009", false},
		{"192.168.1.10:10009", false},
		{"example.com:10009", false},
		{"", false},
	} {
		require.Equal(t, tc.want, isLoopbackAddress(tc.addr), "addr=%q", tc.addr)
	}
}

// TestNewFLNDclient_NonLoopbackWithoutCertErrors pins LOKIHUB-0037: the admin
// macaroon must not be sent over an unverified channel. With no pinned cert, a
// non-loopback address is refused rather than dialed with InsecureSkipVerify.
func TestNewFLNDclient_NonLoopbackWithoutCertErrors(t *testing.T) {
	_, err := NewFLNDclient(FLNDoptions{
		Address:     "flnd:10005",
		CertHex:     "",
		MacaroonHex: testMacaroonHex(t),
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "non-loopback")
	require.ErrorContains(t, err, "pinned TLS certificate")
}

// TestNewFLNDclient_LoopbackWithoutCertAllowed confirms local dev still works:
// a loopback address without a pinned cert is allowed (grpc.NewClient is lazy,
// so no connection is attempted here).
func TestNewFLNDclient_LoopbackWithoutCertAllowed(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:10009", "localhost:10009"} {
		w, err := NewFLNDclient(FLNDoptions{
			Address:     addr,
			CertHex:     "",
			MacaroonHex: testMacaroonHex(t),
		})
		require.NoError(t, err, "addr=%q", addr)
		require.NotNil(t, w)
		require.NoError(t, w.Close())
	}
}
