package manager

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/persist"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Extending mockLNClient to support ConnectionManager testing
type mockLNClientConnection struct {
	mockLNClient
	peers             []lnclient.PeerDetails
	connectedPeers    map[string]string                       // pubkey -> address (legacy for other tests)
	peerRequests      map[string]*lnclient.ConnectPeerRequest // capture requests for detailed verification
	connectPeerCalled bool
	connectPeerErr    error
}

func (m *mockLNClientConnection) ListPeers(ctx context.Context) ([]lnclient.PeerDetails, error) {
	return m.peers, nil
}

func (m *mockLNClientConnection) ConnectPeer(ctx context.Context, req *lnclient.ConnectPeerRequest) error {
	m.connectPeerCalled = true
	if m.connectPeerErr != nil {
		return m.connectPeerErr
	}
	m.connectedPeers[req.Pubkey] = req.Address
	m.peerRequests[req.Pubkey] = req
	return nil
}

func setupTestEnvironment(t *testing.T) (*mockLNClientConnection, *LSPManager, *ConnectionManager) {
	// Setup DB
	// Use unique memory DB to avoid shared state if run in parallel or shared cache
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open valid DB: %v", err)
	}
	if err := db.AutoMigrate(&persist.LSP{}); err != nil {
		t.Fatalf("Failed to migrate DB: %v", err)
	}

	lspManager := NewLSPManager(db)

	// Setup Mock LN
	mockLN := &mockLNClientConnection{
		connectedPeers: make(map[string]string),
		peerRequests:   make(map[string]*lnclient.ConnectPeerRequest),
	}

	cfg := &ManagerConfig{
		LNClient:   mockLN,
		LSPManager: lspManager,
	}

	cm := NewConnectionManager(cfg)

	return mockLN, lspManager, cm
}

func TestMaintainConnections_NoLSPs(t *testing.T) {
	mockLN, _, cm := setupTestEnvironment(t)

	// Action
	cm.maintainConnections(context.Background())

	// Assert
	if mockLN.connectPeerCalled {
		t.Error("ConnectPeer should not be called when no LSPs exist")
	}
}

func TestMaintainConnections_InactiveLSPs(t *testing.T) {
	mockLN, lspManager, cm := setupTestEnvironment(t)

	// Setup
	_, err := lspManager.AddLSP("InactiveLSP", "pubkey1", "1.2.3.4:5521", false, false)
	if err != nil {
		t.Fatalf("Failed to add LSP: %v", err)
	}

	// Action
	cm.maintainConnections(context.Background())

	// Assert
	if mockLN.connectPeerCalled {
		t.Error("ConnectPeer should not be called for inactive LSPs")
	}
}

func TestMaintainConnections_ActiveLSP_AlreadyConnected(t *testing.T) {
	mockLN, lspManager, cm := setupTestEnvironment(t)

	pubkey := "pubkey_active"
	_, err := lspManager.AddLSP("ActiveLSP", pubkey, "1.2.3.4:5521", true, false)
	if err != nil {
		t.Fatalf("Failed to add LSP: %v", err)
	}

	// Mock already connected
	mockLN.peers = []lnclient.PeerDetails{
		{
			NodeId:      pubkey,
			Address:     "1.2.3.4:5521",
			IsConnected: true,
		},
	}

	// Action
	cm.maintainConnections(context.Background())

	// Assert
	if mockLN.connectPeerCalled {
		t.Error("ConnectPeer should not be called if already connected")
	}
}

func TestMaintainConnections_ActiveLSP_Disconnected_Success(t *testing.T) {
	mockLN, lspManager, cm := setupTestEnvironment(t)

	pubkey := "pubkey_disconnected"
	host := "1.2.3.4:5521"
	_, err := lspManager.AddLSP("DisconnectedLSP", pubkey, host, true, false)
	if err != nil {
		t.Fatalf("Failed to add LSP: %v", err)
	}

	// Mock NOT connected (empty peers)
	mockLN.peers = []lnclient.PeerDetails{}

	// Action
	cm.maintainConnections(context.Background())

	// Assert
	if !mockLN.connectPeerCalled {
		t.Error("ConnectPeer should have been called")
	}
	if req, ok := mockLN.peerRequests[pubkey]; !ok {
		t.Errorf("Expected connection attempt to %s, got none", pubkey)
	} else {
		// Expect split host and port
		expectedHost := "1.2.3.4"
		expectedPort := uint16(5521)
		if req.Address != expectedHost {
			t.Errorf("Expected Address %s, got %s", expectedHost, req.Address)
		}
		if req.Port != expectedPort {
			t.Errorf("Expected Port %d, got %d", expectedPort, req.Port)
		}
	}
}

func TestMaintainConnections_ActiveLSP_Disconnected_Failure(t *testing.T) {
	mockLN, lspManager, cm := setupTestEnvironment(t)

	pubkey := "pubkey_fail"
	_, err := lspManager.AddLSP("FailLSP", pubkey, "1.2.3.4:5521", true, false)
	if err != nil {
		t.Fatalf("Failed to add LSP: %v", err)
	}

	// Mock NOT connected
	mockLN.peers = []lnclient.PeerDetails{}
	// Mock Failure
	mockLN.connectPeerErr = fmt.Errorf("connection refused")

	// Action
	cm.maintainConnections(context.Background())

	// Assert
	if !mockLN.connectPeerCalled {
		t.Error("ConnectPeer should have been called despite failure")
	}
	// We expect NO panic and smooth handling (logging error in real impl)
}

func TestMaintainConnections_SyncsCaseInsensitively(t *testing.T) {
	mockLN, lspManager, cm := setupTestEnvironment(t)

	// User adds LSP (stored as lowercase in DB internally by AddLSP)
	pubkeyLower := "03aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := lspManager.AddLSP("MixedCaseLSP", pubkeyLower, "1.2.3.4:5521", true, false)
	if err != nil {
		t.Fatalf("Failed to add LSP: %v", err)
	}

	// Mock LND returning UPPERCASE pubkey (simulating case mismatch)
	pubkeyUpper := strings.ToUpper(pubkeyLower)
	mockLN.peers = []lnclient.PeerDetails{
		{
			NodeId:      pubkeyUpper,
			Address:     "1.2.3.4:5521",
			IsConnected: true,
		},
	}

	// Action
	cm.maintainConnections(context.Background())

	// Assert
	if mockLN.connectPeerCalled {
		t.Error("ConnectPeer should NOT be called; existing upper-case peer should match lower-case LSP record")
	}
}
