package itest

type PaymentReceivedPayload struct {
	Template string `json:"template" binding:"required,eq=payment_received"`
	Data     struct {
		PaymentHash string `json:"payment_hash" binding:"required"`
	} `json:"data"`
}

type TxConfirmedPayload struct {
	Template string `json:"template" binding:"required,eq=tx_confirmed"`
	Data     struct {
		TxID string `json:"tx_id" binding:"required"`
	} `json:"data"`
}

type AddressTxsChangedPayload struct {
	Data struct {
		Address string `json:"address" binding:"required"`
	} `json:"data"`
}

type Lsps5Notification struct {
	JsonRpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}
