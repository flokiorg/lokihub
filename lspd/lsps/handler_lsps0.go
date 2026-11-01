package lsps

import (
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/rs/zerolog/log"
)

// handleLsps0ListProtocols returns SupportedProtocols: the protocols this
// node offers right now (F8-Q1).
func (s *Handlers) handleLsps0ListProtocols(req *JsonRpcRequest) *JsonRpcResponse {
	log.Trace().
		Str("component", string(logging.LogComponentLSPS0)).
		Str("action", string(logging.LogActionRequest)).
		Msg("LSPS0: Received list_protocols request")
	result := map[string][]int{
		"protocols": s.SupportedProtocols(),
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS0)).
		Str("action", string(logging.LogActionResponse)).
		Msgf("LSPS0: list_protocols result: %+v", result)
	return NewJsonRpcResponse(req.Id, result)
}

type Lsps0GetInfoResponse struct {
	SupportedVersions       []int  `json:"supported_versions"`
	NotificationNostrPubkey string `json:"notification_nostr_pubkey,omitempty"`
}

func (s *Handlers) handleLsps0GetInfo(req *JsonRpcRequest) *JsonRpcResponse {
	log.Trace().
		Str("component", string(logging.LogComponentLSPS0)).
		Str("action", string(logging.LogActionRequest)).
		Msg("LSPS0: Received get_info request")

	result := Lsps0GetInfoResponse{
		SupportedVersions: []int{1, 2, 5},
	}

	if s.config.Lsps5NostrPubkey != "" {
		result.NotificationNostrPubkey = s.config.Lsps5NostrPubkey
	}

	log.Trace().
		Str("component", string(logging.LogComponentLSPS0)).
		Str("action", string(logging.LogActionResponse)).
		Msgf("LSPS0: get_info result: %+v", result)
	return NewJsonRpcResponse(req.Id, result)
}
