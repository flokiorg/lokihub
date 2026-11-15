package ports

import "encoding/json"

// LSPSAnswers are a node's current LSPS answers, exactly as the LSPS
// handlers give them to a client (T8-3). The announcement is built from
// them, and the self-check compares it with them (T8-4).
type LSPSAnswers struct {
	// Protocols is the LSPS0 list_protocols list.
	Protocols []int
	// LSPS1Info is the LSPS1 get_info result; set when 1 is listed.
	LSPS1Info json.RawMessage
	// LSPS2Info is the LSPS2 get_info result (opening_fee_params_menu); set
	// when 2 is listed.
	LSPS2Info json.RawMessage
	// LSPS5 holds the LSPS5 fields a set_webhook result carries
	// (max_webhooks); set when 5 is listed.
	LSPS5 json.RawMessage
}

// LSPSAnswerSource gives a node's current LSPS answers.
type LSPSAnswerSource interface {
	LSPSAnswers() (*LSPSAnswers, error)
}
