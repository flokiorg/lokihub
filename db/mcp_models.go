package db

import "time"

// MCPExecution records a write an AI assistant completed, keyed by its
// idempotency key, so a retry after a hub restart returns the first result
// instead of running again (critic O1). Kept for the idempotency window.
type MCPExecution struct {
	ID        uint
	AppID     uint   `gorm:"uniqueIndex:idx_mcp_exec_key,priority:1"`
	Tool      string `gorm:"uniqueIndex:idx_mcp_exec_key,priority:2"`
	Key       string `gorm:"uniqueIndex:idx_mcp_exec_key,priority:3"`
	ArgsHash  string
	Result    string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`
}

// MCPActivity is the owner-visible record of an assistant's tool calls (no
// tool inputs). It deliberately has no foreign key: the record outlives the
// assistant's deletion.
type MCPActivity struct {
	ID        uint
	AppID     uint `gorm:"index"`
	Tool      string
	Outcome   string
	Detail    string
	CreatedAt time.Time `gorm:"index"`
}

// MCPSpend is one assistant-confirmed outflow (amount + maximum routing fee)
// counted against the owner's 24h approval limit; durable so a restart never
// resets the limit.
type MCPSpend struct {
	ID          uint
	AmountMloki uint64
	CreatedAt   time.Time `gorm:"index"`
}
