package manager

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/persist"
)

const gatedLSP = "03dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

// R2-S2: a trusted LSP whose newest announcement is unusable (failed a check,
// expired) or closed gets no zero-conf and is not reconnected through the
// address of an older announcement; a usable open one keeps both.
func TestUnusableTrustedLSPNotConnectedNorZeroConf(t *testing.T) {
	cases := map[string]struct {
		usable bool
		status string
		ok     bool
	}{
		"usable open": {true, "open", true},
		"paused":      {true, "paused", true},
		"unusable":    {false, "open", false},
		"closed":      {true, "closed", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&persist.LSP{}))
			require.NoError(t, db.Create(&persist.LSP{NodeKey: NodeKeyOf(gatedLSP), Pubkey: gatedLSP, Host: "10.0.0.1:9735",
				Trusted: true, Usable: c.usable, Status: c.status}).Error)
			conn := &mockLNClientConnection{connectedPeers: map[string]string{}, peerRequests: map[string]*lnclient.ConnectPeerRequest{}}
			cfg := &ManagerConfig{LNClient: conn, LSPManager: NewLSPManager(db)}
			m := &LiquidityManager{cfg: cfg, nostrPubkeys: map[string]string{}}

			var zeroConf bool
			m.handleChannelAcceptRequest(lnclient.ChannelAcceptRequest{ID: "1", NodePubkey: gatedLSP},
				func(_ string, _ bool, z bool) error { zeroConf = z; return nil })
			assert.Equal(t, c.ok, zeroConf, "zero-conf")

			NewConnectionManager(cfg).maintainConnections(context.TODO())
			assert.Equal(t, c.ok, conn.connectPeerCalled, "reconnected")

			conn.connectPeerCalled = false
			err = m.ConnectLSP(context.TODO(), gatedLSP)
			assert.Equal(t, c.ok, err == nil, "connect on notification: %v", err)
		})
	}
}
