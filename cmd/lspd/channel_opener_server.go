package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	ecies "github.com/flokiorg/lokihub/internal/eciesgo"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	rpc "github.com/flokiorg/lokihub/lspd/api/proto/rpc"
	"github.com/flokiorg/lokihub/lspd/common"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/interceptor"
	"github.com/flokiorg/lokihub/lspd/ports"
	"google.golang.org/protobuf/proto"

	"github.com/flokiorg/flnd/lnwire"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
)

type channelOpenerServer struct {
	rpc.ChannelOpenerServer
	store          interceptor.InterceptStore
	openingService ports.OpeningService
}

func NewChannelOpenerServer(
	store interceptor.InterceptStore,
	openingService ports.OpeningService,
) *channelOpenerServer {
	return &channelOpenerServer{
		store:          store,
		openingService: openingService,
	}
}

func (s *channelOpenerServer) ChannelInformation(ctx context.Context, in *rpc.ChannelInformationRequest) (*rpc.ChannelInformationReply, error) {
	node, token, err := rpc.GetNode(ctx)
	if err != nil {
		return nil, err
	}

	params, err := s.openingService.GetFeeParamsMenu(token, node.PrivateKey)
	if err != nil {
		return nil, err
	}

	var menu []*rpc.OpeningFeeParams
	for _, p := range params {
		menu = append(menu, &rpc.OpeningFeeParams{
			MinMloki:             p.MinFeeMloki,
			Proportional:         p.Proportional,
			ValidUntil:           p.ValidUntil,
			MaxIdleTime:          p.MinLifetime,
			MaxClientToSelfDelay: p.MaxClientToSelfDelay,
			Promise:              p.Promise,
		})
	}

	cfg := node.NodeConfig.Load()
	return &rpc.ChannelInformationReply{
		Name:                   cfg.Name,
		Pubkey:                 cfg.NodePubkey,
		Host:                   cfg.Host,
		ChannelCapacity:        int64(cfg.PublicChannelAmount),
		TargetConf:             int32(cfg.TargetConf),
		MinHtlcMloki:           int64(cfg.MinHtlcMloki),
		BaseFeeMloki:           int64(cfg.BaseFeeMloki),
		FeeRate:                cfg.FeeRate,
		TimeLockDelta:          cfg.TimeLockDelta,
		ChannelFeePermyriad:    int64(cfg.ChannelFeePermyriad),
		ChannelMinimumFeeMloki: int64(cfg.ChannelMinimumFeeMloki),
		LspPubkey:              node.PublicKey.SerializeCompressed(), // TODO: Is the publicKey different from the ecies public key?
		MaxInactiveDuration:    int64(cfg.MaxInactiveDuration),
		OpeningFeeParamsMenu:   menu,
	}, nil
}

func (s *channelOpenerServer) RegisterPayment(
	ctx context.Context,
	in *rpc.RegisterPaymentRequest,
) (*rpc.RegisterPaymentReply, error) {
	node, token, err := rpc.GetNode(ctx)
	if err != nil {
		return nil, err
	}

	data, err := ecies.Decrypt(node.EciesPrivateKey, in.Blob)
	if err != nil {
		log.Error().Msgf("ecies.Decrypt(%x) error: %v", in.Blob, err)
		return nil, fmt.Errorf("ecies.Decrypt(%x) error: %w", in.Blob, err)
	}

	var pi rpc.PaymentInformation
	err = proto.Unmarshal(data, &pi)
	if err != nil {
		// The plaintext carries the payment secret: never log or return it.
		log.Error().Msgf("proto.Unmarshal(%d decrypted bytes) error: %v", len(data), err)
		return nil, fmt.Errorf("proto.Unmarshal(%d decrypted bytes) error: %w", len(data), err)
	}
	log.Info().Msgf("RegisterPayment - Destination: %x, pi.PaymentHash: %x, pi.IncomingAmountMloki: %v, pi.OutgoingAmountMloki: %v, pi.Tag: %v",
		pi.Destination, pi.PaymentHash, pi.IncomingAmountMloki, pi.OutgoingAmountMloki, pi.Tag)

	if len(pi.Tag) > 1000 {
		return nil, fmt.Errorf("tag too long")
	}

	if len(pi.Tag) != 0 {
		var tag json.RawMessage
		err = json.Unmarshal([]byte(pi.Tag), &tag)
		if err != nil {
			return nil, fmt.Errorf("tag is not a valid json object")
		}
	}

	// TODO: Remove this nil check and the else cluase when we enforce all
	// clients to use opening_fee_params.
	if pi.OpeningFeeParams != nil {
		valid := s.openingService.ValidateOpeningFeeParams(
			&core.OpeningFeeParams{
				MinFeeMloki:          pi.OpeningFeeParams.MinMloki,
				Proportional:         pi.OpeningFeeParams.Proportional,
				ValidUntil:           pi.OpeningFeeParams.ValidUntil,
				MinLifetime:          pi.OpeningFeeParams.MaxIdleTime,
				MaxClientToSelfDelay: pi.OpeningFeeParams.MaxClientToSelfDelay,
				Promise:              pi.OpeningFeeParams.Promise,
			},
			node.PublicKey,
		)
		if !valid {
			return nil, fmt.Errorf("invalid opening_fee_params")
		}
	} else {
		tokenHash := sha256.Sum256([]byte(token))
		log.Info().Msgf("DEPRECATED: RegisterPayment with deprecated fee mechanism. sha256 hash of token used: %x", tokenHash)
		cfg := node.NodeConfig.Load()
		pi.OpeningFeeParams = &rpc.OpeningFeeParams{
			MinMloki:             uint64(cfg.ChannelMinimumFeeMloki),
			Proportional:         uint32(cfg.ChannelFeePermyriad * 100),
			ValidUntil:           time.Now().UTC().Add(time.Duration(time.Hour * 24)).Format(common.TimeFormat),
			MaxIdleTime:          uint32(cfg.MaxInactiveDuration / 60), // seconds -> blocks (1 min/block)
			MaxClientToSelfDelay: uint32(10000),
		}
	}

	err = checkPayment(pi.OpeningFeeParams, pi.IncomingAmountMloki, pi.OutgoingAmountMloki)
	if err != nil {
		log.Error().Msgf("checkPayment(%v, %v) error: %v", pi.IncomingAmountMloki, pi.OutgoingAmountMloki, err)
		return nil, fmt.Errorf("checkPayment(%v, %v) error: %v", pi.IncomingAmountMloki, pi.OutgoingAmountMloki, err)
	}
	params := &core.OpeningFeeParams{
		MinFeeMloki:          pi.OpeningFeeParams.MinMloki,
		Proportional:         pi.OpeningFeeParams.Proportional,
		ValidUntil:           pi.OpeningFeeParams.ValidUntil,
		MinLifetime:          pi.OpeningFeeParams.MaxIdleTime,
		MaxClientToSelfDelay: pi.OpeningFeeParams.MaxClientToSelfDelay,
		Promise:              pi.OpeningFeeParams.Promise,
	}
	err = s.store.RegisterPayment(token, "", params, pi.Destination, pi.PaymentHash, pi.PaymentSecret, pi.IncomingAmountMloki, pi.OutgoingAmountMloki, pi.Tag)
	if err != nil {
		log.Error().Msgf("RegisterPayment() error: %v", err)
		return nil, fmt.Errorf("RegisterPayment() error: %w", err)
	}
	return &rpc.RegisterPaymentReply{}, nil
}

func getSignedEncryptedData(n *core.Node, in *rpc.Encrypted) (string, []byte, error) {
	signedBlob, err := ecies.Decrypt(n.EciesPrivateKey, in.Data)
	if err != nil {
		log.Error().Msgf("ecies.Decrypt(%x) error: %v", in.Data, err)
		return "", nil, fmt.Errorf("ecies.Decrypt(%x) error: %w", in.Data, err)
	}
	var signed rpc.Signed
	err = proto.Unmarshal(signedBlob, &signed)
	if err != nil {
		// The plaintext carries the payment secret: never log or return it.
		log.Error().Msgf("proto.Unmarshal(%d decrypted bytes) error: %v", len(signedBlob), err)
		return "", nil, fmt.Errorf("proto.Unmarshal(%d decrypted bytes) error: %w", len(signedBlob), err)
	}
	pubkey, err := btcec.ParsePubKey(signed.Pubkey)
	if err != nil {
		log.Error().Msgf("unable to parse pubkey: %v", err)
		return "", nil, fmt.Errorf("unable to parse pubkey: %w", err)
	}
	wireSig, err := lnwire.NewSigFromECDSARawSignature(signed.Signature)
	if err != nil {
		return "", nil, fmt.Errorf("failed to decode signature: %v", err)
	}
	sig, err := wireSig.ToSignature()
	if err != nil {
		return "", nil, fmt.Errorf("failed to convert from wire format: %v",
			err)
	}
	// The signature is over the sha256 hash of the message.
	digest := chainhash.HashB(signed.Data)
	if !sig.Verify(digest, pubkey) {
		return "", nil, fmt.Errorf("invalid signature")
	}
	return hex.EncodeToString(signed.Pubkey), signed.Data, nil
}

func (s *channelOpenerServer) CheckChannels(ctx context.Context, in *rpc.Encrypted) (*rpc.Encrypted, error) {
	node, _, err := rpc.GetNode(ctx)
	if err != nil {
		return nil, err
	}

	nodeID, data, err := getSignedEncryptedData(node, in)
	if err != nil {
		log.Error().Msgf("getSignedEncryptedData error: %v", err)
		return nil, fmt.Errorf("getSignedEncryptedData error: %v", err)
	}
	var checkChannelsRequest rpc.CheckChannelsRequest
	err = proto.Unmarshal(data, &checkChannelsRequest)
	if err != nil {
		log.Error().Msgf("proto.Unmarshal(%x) error: %v", data, err)
		return nil, fmt.Errorf("proto.Unmarshal(%x) error: %w", data, err)
	}
	closedChannels, err := node.Client.GetClosedChannels(nodeID, checkChannelsRequest.WaitingCloseChannels)
	if err != nil {
		log.Error().Msgf("GetClosedChannels(%v) error: %v", checkChannelsRequest.FakeChannels, err)
		return nil, fmt.Errorf("GetClosedChannels(%v) error: %w", checkChannelsRequest.FakeChannels, err)
	}
	checkChannelsReply := rpc.CheckChannelsReply{
		NotFakeChannels: make(map[string]uint64),
		ClosedChannels:  closedChannels,
	}
	dataReply, err := proto.Marshal(&checkChannelsReply)
	if err != nil {
		log.Error().Msgf("proto.Marshall() error: %v", err)
		return nil, fmt.Errorf("proto.Marshal() error: %w", err)
	}

	var encrypted []byte
	encrypted, err = ecies.Encrypt(node.EciesPublicKey, dataReply)
	if err != nil {
		log.Error().Msgf("ecies.Encrypt() error: %v", err)
		return nil, fmt.Errorf("ecies.Encrypt() error: %w", err)
	}

	return &rpc.Encrypted{Data: encrypted}, nil
}

func checkPayment(params *rpc.OpeningFeeParams, incomingAmountMloki, outgoingAmountMloki int64) error {
	fees := incomingAmountMloki * int64(params.Proportional) / 1_000_000 / 1_000 * 1_000
	if fees < int64(params.MinMloki) {
		fees = int64(params.MinMloki)
	}
	if incomingAmountMloki-outgoingAmountMloki < fees {
		return fmt.Errorf("not enough fees")
	}
	return nil
}
