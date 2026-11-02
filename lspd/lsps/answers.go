package lsps

import (
	"encoding/json"
	"fmt"

	"github.com/flokiorg/lokihub/lspd/ports"
)

var _ ports.LSPSAnswerSource = (*Handlers)(nil)

// LSPSAnswers returns the answers these handlers give a client right now,
// produced by the same handler code a client's request reaches, so the
// announcement built from them equals the live answers by construction
// (T8-3, NIP-LSP §Publishing). The LSPS2 menu is asked without a token,
// as an anonymous client would.
func (s *Handlers) LSPSAnswers() (*ports.LSPSAnswers, error) {
	a := &ports.LSPSAnswers{Protocols: s.SupportedProtocols()}
	for _, p := range a.Protocols {
		var err error
		switch p {
		case 1:
			a.LSPS1Info, err = resultJSON(s.handleLsps1GetInfo(&JsonRpcRequest{Id: json.RawMessage(`"announce"`), Method: "lsps1.get_info"}))
		case 2:
			// Strict: a token's menu that fails to read fails the answers,
			// rather than announcing a menu without its rows.
			a.LSPS2Info, err = resultJSON(s.lsps2GetInfoMenu(&JsonRpcRequest{Id: json.RawMessage(`"announce"`), Method: "lsps2.get_info"}, true))
		case 5:
			// What set_webhook and list_webhooks answer as max_webhooks.
			a.LSPS5, err = json.Marshal(map[string]int{"max_webhooks": s.config.Lsps5MaxWebhooks})
		}
		if err != nil {
			return nil, fmt.Errorf("LSPS%d answer: %w", p, err)
		}
	}
	return a, nil
}

func resultJSON(resp *JsonRpcResponse) (json.RawMessage, error) {
	if resp == nil {
		return nil, fmt.Errorf("no response")
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return json.Marshal(resp.Result)
}
