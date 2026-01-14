// Package transport provides LSPS message transport over Lightning Network custom messages
package transport

import (
	"context"
	"fmt"

	"github.com/flokiorg/lokihub/lnclient"
)

// CustomMessage represents a custom protocol message
type CustomMessage struct {
	PeerPubkey string
	Type       uint32
	Data       []byte
}

// MessageSender sends custom messages to peers
type MessageSender interface {
	SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error
}

// MessageReceiver receives custom messages from peers
type MessageReceiver interface {
	SubscribeCustomMessages(ctx context.Context) (<-chan CustomMessage, <-chan error, error)
}

// Transport combines sending and receiving capabilities
type Transport interface {
	MessageSender
	MessageReceiver
}

// LNDTransport implements Transport using an LNClient
type LNDTransport struct {
	lnClient lnclient.LNClient
}

// NewLNDTransport creates a new LND-based transport
func NewLNDTransport(lnClient lnclient.LNClient) *LNDTransport {
	return &LNDTransport{
		lnClient: lnClient,
	}
}

// SendCustomMessage sends a custom message to a peer
func (t *LNDTransport) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	if len(data) > 65535 {
		return fmt.Errorf("message too large: %d bytes (max 65535)", len(data))
	}
	return t.lnClient.SendCustomMessage(ctx, peerPubkey, msgType, data)
}

// SubscribeCustomMessages subscribes to incoming custom messages
func (t *LNDTransport) SubscribeCustomMessages(ctx context.Context) (<-chan CustomMessage, <-chan error, error) {
	msgChan, errChan, err := t.lnClient.SubscribeCustomMessages(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Convert lnclient.CustomMessage to transport.CustomMessage
	transportMsgChan := make(chan CustomMessage, 100)

	go func() {
		defer close(transportMsgChan)
		for {
			select {
			case msg, ok := <-msgChan:
				if !ok {
					return
				}
				transportMsgChan <- CustomMessage{
					PeerPubkey: msg.PeerPubkey,
					Type:       msg.Type,
					Data:       msg.Data,
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return transportMsgChan, errChan, nil
}
