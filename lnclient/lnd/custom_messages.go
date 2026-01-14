package lnd

import (
	"context"
	"encoding/hex"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/sirupsen/logrus"
)

// SendCustomMessage sends a custom peer message to the specified peer.
// This is used for LSPS protocol communication over the Lightning Network.
func (svc *LNDService) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	peerPubkeyBytes, err := hex.DecodeString(peerPubkey)
	if err != nil {
		logger.Logger.WithFields(logrus.Fields{
			"peer_pubkey": peerPubkey,
			"msg_type":    msgType,
		}).WithError(err).Error("Failed to decode peer pubkey")
		return err
	}

	req := &lnrpc.SendCustomMessageRequest{
		Peer: peerPubkeyBytes,
		Type: msgType,
		Data: data,
	}

	_, err = svc.client.SendCustomMessage(ctx, req)
	if err != nil {
		logger.Logger.WithFields(logrus.Fields{
			"peer_pubkey": peerPubkey,
			"msg_type":    msgType,
		}).WithError(err).Error("Failed to send custom message")
		return err
	}

	logger.Logger.WithFields(logrus.Fields{
		"peer_pubkey": peerPubkey,
		"msg_type":    msgType,
		"data_len":    len(data),
	}).Debug("Sent custom message")

	return nil
}

// SubscribeCustomMessages subscribes to incoming custom messages from peers.
// Returns a channel for messages, a channel for errors, and an error if subscription fails.
func (svc *LNDService) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	msgChan := make(chan lnclient.CustomMessage, 100)
	errChan := make(chan error, 1)

	stream, err := svc.client.SubscribeCustomMessages(ctx, &lnrpc.SubscribeCustomMessagesRequest{})
	if err != nil {
		logger.Logger.WithError(err).Error("Failed to subscribe to custom messages")
		close(msgChan)
		close(errChan)
		return msgChan, errChan, err
	}

	go func() {
		defer close(msgChan)
		defer close(errChan)

		for {
			msg, err := stream.Recv()
			if err != nil {
				if ctx.Err() != nil {
					// Context cancelled, exit gracefully
					return
				}
				logger.Logger.WithError(err).Error("Error receiving custom message")
				select {
				case errChan <- err:
				case <-ctx.Done():
					return
				}
				return
			}

			peerPubkey := hex.EncodeToString(msg.Peer)
			customMsg := lnclient.CustomMessage{
				PeerPubkey: peerPubkey,
				Type:       msg.Type,
				Data:       msg.Data,
			}

			select {
			case msgChan <- customMsg:
				logger.Logger.WithFields(logrus.Fields{
					"peer_pubkey": peerPubkey,
					"msg_type":    msg.Type,
					"data_len":    len(msg.Data),
				}).Debug("Received custom message")
			case <-ctx.Done():
				return
			}
		}
	}()

	return msgChan, errChan, nil
}
