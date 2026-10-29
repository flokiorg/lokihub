// Package transport provides LSPS message transport over Lightning Network custom messages
package transport

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// FLNDTransport implements Transport using an LNClient
type FLNDTransport struct {
	lnClient lnclient.LNClient
}

// NewFLNDTransport creates a new FLND-based transport
func NewFLNDTransport(lnClient lnclient.LNClient) *FLNDTransport {
	return &FLNDTransport{
		lnClient: lnClient,
	}
}

// SendCustomMessage sends a custom message to a peer
func (t *FLNDTransport) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	if len(data) > 65535 {
		return fmt.Errorf("message too large: %d bytes (max 65535)", len(data))
	}
	// A peer whose connection is being replaced (a reconnect racing an old
	// connection, or a disconnect just before) refuses the message for a moment;
	// the node reconnects a persistent peer, so the send is retried briefly.
	var err error
	for attempt := 0; attempt < sendAttempts; attempt++ {
		if err = t.lnClient.SendCustomMessage(ctx, peerPubkey, msgType, data); err == nil || !peerChurning(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(sendRetryDelay):
		}
	}
	return err
}

// sendAttempts and sendRetryDelay bound SendCustomMessage's retries while a
// peer's connection is being replaced.
const (
	sendAttempts   = 4
	sendRetryDelay = 750 * time.Millisecond
)

// peerChurning reports the node's errors for a peer whose connection is going
// or being replaced.
func peerChurning(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "peer exiting") || strings.Contains(msg, "disconnected") ||
		strings.Contains(msg, "not online") || strings.Contains(msg, "not connected")
}

// SubscribeCustomMessages subscribes to incoming custom messages
func (t *FLNDTransport) SubscribeCustomMessages(ctx context.Context) (<-chan CustomMessage, <-chan error, error) {
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
