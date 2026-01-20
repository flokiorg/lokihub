package db

import (
	"time"

	"gorm.io/datatypes"
)

type UserConfig struct {
	ID        uint
	Key       string `gorm:"unique;not null"`
	Value     string
	Encrypted bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type App struct {
	ID           uint
	Name         string `validate:"required"`
	Description  string
	AppPubkey    string `validate:"required" gorm:"unique;not null"`
	WalletPubkey *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastUsedAt   *time.Time
	Isolated     bool
	Metadata     datatypes.JSON
}

type AppPermission struct {
	ID            uint
	AppId         uint   `validate:"required"`
	App           App    `gorm:"constraint:OnDelete:CASCADE;"`
	Scope         string `validate:"required"`
	MaxAmountLoki int
	BudgetRenewal string
	ExpiresAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type RequestEvent struct {
	ID          uint
	AppId       *uint
	App         App    `gorm:"constraint:OnDelete:CASCADE;"`
	NostrId     string `validate:"required" gorm:"unique;not null"`
	ContentData string
	Method      string
	State       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ResponseEvent struct {
	ID           uint
	NostrId      string       `validate:"required" gorm:"unique;not null"`
	RequestId    uint         `validate:"required"`
	RequestEvent RequestEvent `gorm:"constraint:OnDelete:CASCADE;foreignKey:RequestId"`
	State        string
	RepliedAt    time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Transaction struct {
	ID              uint
	AppId           *uint
	App             *App
	RequestEventId  *uint
	RequestEvent    *RequestEvent
	Type            string
	State           string
	AmountMloki     uint64 `gorm:"column:amount_mloki"`
	FeeMloki        uint64
	FeeReserveMloki uint64
	PaymentRequest  string
	PaymentHash     string
	Description     string
	DescriptionHash string
	Preimage        *string
	CreatedAt       time.Time
	ExpiresAt       *time.Time
	UpdatedAt       time.Time
	SettledAt       *time.Time
	Metadata        datatypes.JSON
	SelfPayment     bool
	Boostagram      datatypes.JSON
	FailureReason   string
	Hold            bool
	SettleDeadline  *uint32 // block number for accepted hold invoices
}

type Swap struct {
	ID                 uint
	SwapId             string `validate:"required" gorm:"unique;not null"`
	Type               string
	State              string
	Invoice            string
	SendAmount         uint64
	ReceiveAmount      uint64
	Preimage           string
	PaymentHash        string
	DestinationAddress string
	RefundAddress      string
	LockupAddress      string
	LockupTxId         string
	ClaimTxId          string
	AutoSwap           bool
	UsedXpub           bool
	TimeoutBlockHeight uint32
	BoltzPubkey        string
	SwapTree           datatypes.JSON
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Forward struct {
	ID                           uint
	OutboundAmountForwardedMloki uint64
	TotalFeeEarnedMloki          uint64
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

const (
	REQUEST_EVENT_STATE_HANDLER_EXECUTING = "executing"
	REQUEST_EVENT_STATE_HANDLER_EXECUTED  = "executed"
	REQUEST_EVENT_STATE_HANDLER_ERROR     = "error"
)
const (
	RESPONSE_EVENT_STATE_PUBLISH_CONFIRMED   = "confirmed"
	RESPONSE_EVENT_STATE_PUBLISH_FAILED      = "failed"
	RESPONSE_EVENT_STATE_PUBLISH_UNCONFIRMED = "unconfirmed"
)
