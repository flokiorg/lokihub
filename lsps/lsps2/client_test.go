package lsps2

import (
	"context"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/transport"
	"github.com/stretchr/testify/mock"
)

// Mock Transport
type MockTransport struct {
	mock.Mock
}

func (m *MockTransport) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	args := m.Called(ctx, peerPubkey, msgType, data)
	return args.Error(0)
}

func (m *MockTransport) SubscribeCustomMessages(ctx context.Context) (<-chan transport.CustomMessage, <-chan error, error) {
	args := m.Called(ctx)
	return nil, nil, args.Error(2)
}

func TestClientHandler_PrunePendingRequests(t *testing.T) {
	trans := new(MockTransport)
	queue := events.NewEventQueue(10)
	client := NewClientHandler(trans, queue)

	peerID := "03abc"

	// Manually inject stale requests
	// We need to use Locking since we are accessing internal state
	client.mu.Lock()
	client.perPeerState[peerID] = &PeerState{
		PendingGetInfoRequests: make(map[string]PendingRequest),
		PendingBuyRequests:     make(map[string]*InboundJITChannel),
	}

	peerState := client.perPeerState[peerID]
	client.mu.Unlock()

	// Add a stale request (1 hour old)
	peerState.mu.Lock()
	peerState.PendingGetInfoRequests["stale-req"] = PendingRequest{
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	// Add a fresh request (1 minute old)
	peerState.PendingGetInfoRequests["fresh-req"] = PendingRequest{
		CreatedAt: time.Now().Add(-1 * time.Minute),
	}
	peerState.mu.Unlock()

	// Prune requests older than 30 minutes
	client.PrunePendingRequests(30 * time.Minute)

	// Verify
	peerState.mu.Lock()
	defer peerState.mu.Unlock()

	if _, ok := peerState.PendingGetInfoRequests["stale-req"]; ok {
		t.Error("Stale request was not pruned")
	}
	if _, ok := peerState.PendingGetInfoRequests["fresh-req"]; !ok {
		t.Error("Fresh request was incorrectly pruned")
	}
}
