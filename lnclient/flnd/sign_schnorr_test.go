package flnd

import (
	"context"
	"errors"
	"testing"

	"github.com/flokiorg/flnd/lnrpc/signrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/flokiorg/lokihub/lnclient/flnd/wrapper"
)

// fakeSigner answers SignMessage; every other method panics via the nil
// embedded interface.
type fakeSigner struct {
	signrpc.SignerClient
	sig   []byte
	err   error
	asked *signrpc.SignMessageReq
}

func (f *fakeSigner) SignMessage(_ context.Context, req *signrpc.SignMessageReq, _ ...grpc.CallOption) (*signrpc.SignMessageResp, error) {
	f.asked = req
	if f.err != nil {
		return nil, f.err
	}
	return &signrpc.SignMessageResp{Signature: f.sig}, nil
}

func newSigningFLND(signer *fakeSigner) *FLNDService {
	client := wrapper.NewFLNDWrapperWithClients(&fakeLightning{}, &fakeRouter{}).WithSigner(signer)
	return &FLNDService{client: client, ctx: context.Background()}
}

func TestSignSchnorrNodeKey_AsksForASingleHashSchnorrSignatureByTheNodeKey(t *testing.T) {
	signer := &fakeSigner{sig: []byte{1, 2, 3}}
	sig, err := newSigningFLND(signer).SignSchnorrNodeKey(context.Background(), []byte("payload"))
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, sig)
	assert.Equal(t, []byte("payload"), signer.asked.Msg)
	assert.True(t, signer.asked.SchnorrSig)
	assert.False(t, signer.asked.DoubleHash)
	assert.Equal(t, int32(6), signer.asked.KeyLoc.KeyFamily, "node key family")
}

func TestSignSchnorrNodeKey_NamesWhatTheNodeIsMissing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		// What flnd's macaroon check returns: a plain error, so gRPC sends it as Unknown.
		{"macaroon lacks signer:generate", status.Error(codes.Unknown, "permission denied"), "signer:generate"},
		{"permission denied code", status.Error(codes.PermissionDenied, "denied"), "signer:generate"},
		{"no signer RPC", status.Error(codes.Unimplemented, "unknown service signrpc.Signer"), "signrpc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newSigningFLND(&fakeSigner{err: tc.err}).SignSchnorrNodeKey(context.Background(), []byte("x"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.ErrorIs(t, err, tc.err, "the node's own error is kept")
		})
	}
}

func TestSignSchnorrNodeKey_PassesOtherErrorsThrough(t *testing.T) {
	nodeErr := errors.New("connection refused")
	_, err := newSigningFLND(&fakeSigner{err: nodeErr}).SignSchnorrNodeKey(context.Background(), []byte("x"))
	assert.Equal(t, nodeErr, err)
}
