package notifications

import (
	context "context"
	"encoding/hex"
	"fmt"

	ecies "github.com/flokiorg/lokihub/internal/eciesgo"
	pb "github.com/flokiorg/lokihub/lspd/api/proto/notifications"
	rpc "github.com/flokiorg/lokihub/lspd/api/proto/rpc"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/proto"
)

var ErrInternal = fmt.Errorf("internal error")

type server struct {
	store Store
	pb.NotificationsServer
}

func NewNotificationsServer(store Store) pb.NotificationsServer {
	return &server{
		store: store,
	}
}

func (s *server) SubscribeNotifications(
	ctx context.Context,
	in *pb.EncryptedNotificationRequest,
) (*pb.SubscribeNotificationsReply, error) {
	node, _, err := rpc.GetNode(ctx)
	if err != nil {
		return nil, err
	}

	data, err := ecies.Decrypt(node.EciesPrivateKey, in.Blob)
	if err != nil {
		log.Warn().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionValidate)).
			Msg("Notifications: Failed to decrypt request")
		return nil, fmt.Errorf("ecies.Decrypt(%x) error: %w", in.Blob, err)
	}

	var request pb.SubscribeNotificationsRequest
	err = proto.Unmarshal(data, &request)
	if err != nil {
		log.Warn().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionValidate)).
			Msg("Notifications: Failed to unmarshal subscription request")
		return nil, fmt.Errorf("proto.Unmarshal(%x) error: %w", data, err)
	}

	pubkey, err := lightning.VerifyMessage([]byte(request.Url), request.Signature)
	if err != nil {
		log.Warn().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionValidate)).
			Str("url", request.Url).
			Msg("Notifications: Failed to verify signature")

		return nil, err
	}

	pubkeyStr := hex.EncodeToString(pubkey.SerializeCompressed())

	// Default to "push" if transport not specified
	transport := request.Transport
	if transport == "" {
		transport = "push"
	}

	err = s.store.Register(ctx, pubkeyStr, request.Url, transport)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionFailure)).
			Str("pubkey", pubkeyStr).
			Str("url", request.Url).
			Msg("Notifications: Failed to register in store")

		return nil, ErrInternal
	}

	log.Debug().
		Str("component", string(logging.LogComponentNotifications)).
		Str("action", string(logging.LogActionSuccess)).
		Str("pubkey", pubkeyStr).
		Str("url", request.Url).
		Str("transport", transport).
		Msg("Notifications: Successfully registered client")
	return &pb.SubscribeNotificationsReply{}, nil
}

func (s *server) UnsubscribeNotifications(
	ctx context.Context,
	in *pb.EncryptedNotificationRequest,
) (*pb.UnsubscribeNotificationsReply, error) {
	node, _, err := rpc.GetNode(ctx)
	if err != nil {
		return nil, err
	}

	data, err := ecies.Decrypt(node.EciesPrivateKey, in.Blob)
	if err != nil {
		log.Warn().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionValidate)).
			Msg("Notifications: Failed to decrypt unsubscribe request")
		return nil, fmt.Errorf("ecies.Decrypt(%x) error: %w", in.Blob, err)
	}

	var request pb.UnsubscribeNotificationsRequest
	err = proto.Unmarshal(data, &request)
	if err != nil {
		log.Warn().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionValidate)).
			Msg("Notifications: Failed to unmarshal unsubscribe request")
		return nil, fmt.Errorf("proto.Unmarshal(%x) error: %w", data, err)
	}

	pubkey, err := lightning.VerifyMessage([]byte(request.Url), request.Signature)
	if err != nil {
		log.Warn().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionValidate)).
			Str("url", request.Url).
			Msg("Notifications: Failed to verify signature for unsubscribe")

		return nil, err
	}

	pubkeyStr := hex.EncodeToString(pubkey.SerializeCompressed())
	err = s.store.Unsubscribe(ctx, pubkeyStr, request.Url)

	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentNotifications)).
			Str("action", string(logging.LogActionFailure)).
			Str("pubkey", pubkeyStr).
			Str("url", request.Url).
			Msg("Notifications: Failed to unsubscribe in store")

		return nil, ErrInternal
	}

	log.Debug().
		Str("component", string(logging.LogComponentNotifications)).
		Str("action", string(logging.LogActionSuccess)).
		Str("pubkey", pubkeyStr).
		Str("url", request.Url).
		Msg("Notifications: Successfully unsubscribed client")

	return &pb.UnsubscribeNotificationsReply{}, nil
}
