package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	ecies "github.com/flokiorg/lokihub/lspd/eciesgo"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	rpc "github.com/flokiorg/lokihub/lspd/api/proto/rpc"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/interceptor"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// recordingStore keeps the secret RegisterPayment was given.
type recordingStore struct {
	interceptor.InterceptStore
	secret []byte
}

func (s *recordingStore) RegisterPayment(_ string, _ string, _ *core.OpeningFeeParams, _, _, paymentSecret []byte, _, _ int64, _ string) error {
	s.secret = paymentSecret
	return nil
}

type acceptingOpeningService struct{ ports.OpeningService }

func (acceptingOpeningService) ValidateOpeningFeeParams(*core.OpeningFeeParams, *btcec.PublicKey) bool {
	return true
}

// captureLog sends the global logger to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })
	return &buf
}

func eciesNode(t *testing.T) (*core.Node, *ecies.PrivateKey) {
	t.Helper()
	key, err := ecies.GenerateKey()
	require.NoError(t, err)
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	return &core.Node{EciesPrivateKey: key, EciesPublicKey: key.PublicKey, PublicKey: priv.PubKey()}, key
}

var testSecret = bytes.Repeat([]byte{0x5e}, 32)

func TestRegisterPayment_StoresButNeverLogsThePaymentSecret(t *testing.T) {
	buf := captureLog(t)
	node, key := eciesNode(t)
	store := &recordingStore{}
	srv := NewChannelOpenerServer(store, acceptingOpeningService{})

	pi := &rpc.PaymentInformation{
		PaymentHash:         bytes.Repeat([]byte{0x11}, 32),
		PaymentSecret:       testSecret,
		Destination:         bytes.Repeat([]byte{0x02}, 33),
		IncomingAmountMloki: 2_000_000,
		OutgoingAmountMloki: 1_000_000,
		OpeningFeeParams:    &rpc.OpeningFeeParams{MinMloki: 1000, Proportional: 1000},
	}
	plain, err := proto.Marshal(pi)
	require.NoError(t, err)
	blob, err := ecies.Encrypt(key.PublicKey, plain)
	require.NoError(t, err)

	_, err = srv.RegisterPayment(rpc.WithNode(context.Background(), node, "tok"), &rpc.RegisterPaymentRequest{Blob: blob})
	require.NoError(t, err)

	assert.Equal(t, testSecret, store.secret, "the secret still reaches the store")
	assert.Contains(t, buf.String(), hex.EncodeToString(pi.PaymentHash), "the payment is still logged")
	assert.NotContains(t, buf.String(), hex.EncodeToString(testSecret))
}

func TestRegisterPayment_UnreadablePlaintextIsNotLoggedNorReturned(t *testing.T) {
	buf := captureLog(t)
	node, key := eciesNode(t)
	srv := NewChannelOpenerServer(&recordingStore{}, acceptingOpeningService{})

	// Not a protobuf message, but it carries the secret bytes.
	plain := append([]byte{0xff, 0xff, 0xff}, testSecret...)
	blob, err := ecies.Encrypt(key.PublicKey, plain)
	require.NoError(t, err)

	_, err = srv.RegisterPayment(rpc.WithNode(context.Background(), node, "tok"), &rpc.RegisterPaymentRequest{Blob: blob})
	require.Error(t, err)

	secretHex := hex.EncodeToString(testSecret)
	assert.Contains(t, buf.String(), "proto.Unmarshal", "the failure is still logged")
	assert.NotContains(t, buf.String(), secretHex)
	assert.NotContains(t, err.Error(), secretHex)
}
