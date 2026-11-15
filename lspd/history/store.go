package history

import (
	"context"
	"time"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/lightning"
)

type ChannelUpdate struct {
	NodeID        []byte
	PeerId        []byte
	AliasScid     *lightning.ShortChannelID
	ConfirmedScid *lightning.ShortChannelID
	ChannelPoint  *wire.OutPoint
	LastUpdate    time.Time
}

type Forward struct {
	Identifier   string
	InChannel    lightning.ShortChannelID
	OutChannel   lightning.ShortChannelID
	InMloki      uint64
	OutMloki     uint64
	ResolvedTime time.Time
}

type OpenChannelHtlc struct {
	NodeId              []byte
	PeerId              []byte
	ChannelPoint        *wire.OutPoint
	OriginalAmountMloki uint64
	ForwardAmountMloki  uint64
	IncomingAmountMloki uint64
	ForwardTime         time.Time
}

type Store interface {
	UpdateChannels(ctx context.Context, updates []*ChannelUpdate) error
	InsertForwards(ctx context.Context, forwards []*Forward, nodeId []byte) error
	UpdateForwards(ctx context.Context, forwards []*Forward, nodeId []byte) error
	FetchLndForwardOffset(ctx context.Context, nodeId []byte) (*time.Time, error)
	AddOpenChannelHtlc(ctx context.Context, htlc *OpenChannelHtlc) error
}
