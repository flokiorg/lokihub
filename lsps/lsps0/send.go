package lsps0

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokiorg/lokihub/lsps/transport"
)

// SendRequest encodes a JSON-RPC request and sends it to peerPubkey as an LSPS
// custom message. Every LSPS client (lsps1, lsps2, lsps5) sends this way.
func SendRequest(ctx context.Context, t transport.Transport, peerPubkey string, request *JsonRpcRequest) error {
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	if err := t.SendCustomMessage(ctx, peerPubkey, LSPS_MESSAGE_TYPE_ID, data); err != nil {
		return fmt.Errorf("failed to send custom message: %w", err)
	}
	return nil
}
