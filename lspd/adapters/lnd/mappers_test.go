package lnd

import (
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapScidsFromChannel(t *testing.T) {
	tests := []struct {
		name              string
		channel           *lnrpc.Channel
		wantAlias         *uint64
		wantConfirmedScid *uint64
	}{
		{
			name: "zero-conf with confirmed scid assigned",
			channel: &lnrpc.Channel{
				ZeroConf:              true,
				ZeroConfConfirmedScid: 123456,
				ChanId:                999999, // the phantom/alias scid for zero-conf
			},
			wantAlias:         ptr(uint64(999999)),
			wantConfirmedScid: ptr(uint64(123456)),
		},
		{
			name: "zero-conf not yet confirmed on-chain",
			channel: &lnrpc.Channel{
				ZeroConf:              true,
				ZeroConfConfirmedScid: 0,
				ChanId:                999999,
			},
			wantAlias:         ptr(uint64(999999)),
			wantConfirmedScid: nil,
		},
		{
			name: "standard channel, no alias scids",
			channel: &lnrpc.Channel{
				ZeroConf: false,
				ChanId:   555555,
			},
			wantAlias:         nil,
			wantConfirmedScid: ptr(uint64(555555)),
		},
		{
			name: "standard channel with an alias scid",
			channel: &lnrpc.Channel{
				ZeroConf:   false,
				ChanId:     555555,
				AliasScids: []uint64{111111, 222222},
			},
			wantAlias:         ptr(uint64(111111)), // only the first is used
			wantConfirmedScid: ptr(uint64(555555)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alias, confirmed := mapScidsFromChannel(tt.channel)

			if tt.wantAlias == nil {
				assert.Nil(t, alias)
			} else {
				require.NotNil(t, alias)
				assert.Equal(t, lightning.ShortChannelID(*tt.wantAlias), *alias)
			}

			if tt.wantConfirmedScid == nil {
				assert.Nil(t, confirmed)
			} else {
				require.NotNil(t, confirmed)
				assert.Equal(t, lightning.ShortChannelID(*tt.wantConfirmedScid), *confirmed)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestExtractChannelPoint(t *testing.T) {
	validHash := chainhash.Hash{0x01, 0x02, 0x03}

	tests := []struct {
		name    string
		cp      *lnrpc.ChannelPoint
		want    string
		wantErr bool
	}{
		{
			name: "funding txid as string, used directly",
			cp: &lnrpc.ChannelPoint{
				FundingTxid: &lnrpc.ChannelPoint_FundingTxidStr{FundingTxidStr: "deadbeef"},
				OutputIndex: 2,
			},
			want: "deadbeef:2",
		},
		{
			name: "funding txid as bytes, converted to hash string",
			cp: &lnrpc.ChannelPoint{
				FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{FundingTxidBytes: validHash[:]},
				OutputIndex: 0,
			},
			want: validHash.String() + ":0",
		},
		{
			name: "invalid funding txid bytes returns an error",
			cp: &lnrpc.ChannelPoint{
				FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{FundingTxidBytes: []byte{0x01, 0x02}}, // too short
				OutputIndex: 0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractChannelPoint(tt.cp)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertInvoiceState(t *testing.T) {
	tests := []struct {
		name  string
		state lnrpc.Invoice_InvoiceState
		want  lightning.InvoiceState
	}{
		{"open", lnrpc.Invoice_OPEN, lightning.InvoiceStateOpen},
		{"settled", lnrpc.Invoice_SETTLED, lightning.InvoiceStateSettled},
		{"canceled", lnrpc.Invoice_CANCELED, lightning.InvoiceStateCanceled},
		{"accepted", lnrpc.Invoice_ACCEPTED, lightning.InvoiceStateAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, convertInvoiceState(tt.state))
		})
	}
}
