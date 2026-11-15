package interceptor

import (
	"testing"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestRecoverStuckLsps2Registrations_ChannelExists_ConsumesRegistration is
// the regression test for T7-13's primary case: flspd crashed AFTER
// openChannel succeeded (a real channel exists to the peer) but BEFORE
// ConsumeLsps2Registration committed. Recovery must find the real channel
// and finish the CONSUMED transition instead of leaving the registration
// stuck at OPENING forever (unreachable, unaccounted for in reserved
// liquidity) or -- worse -- releasing it back to ACTIVE, which would
// reopen F-15's exact amplification bug for a channel that already exists.
func TestRecoverStuckLsps2Registrations_ChannelExists_ConsumesRegistration(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)

	destination := fakePubkey(0x11)
	paymentSize := uint64(20_000_000)
	stuck := &core.Lsps2BuyRegistration{
		Id:                 "reg-stuck-1",
		PeerId:             hexEncode(destination),
		Scid:               12345,
		PaymentSizeMloki:   &paymentSize,
		ParamsMinFeeMloki:  1000,
		ReservedAmountLoki: 20_000,
		State:              core.Lsps2RegistrationOpening,
	}
	store.On("GetStuckLsps2Registrations").Return([]*core.Lsps2BuyRegistration{stuck}, nil)
	store.On("GetClaimedLsps2ChannelPoints").Return(map[string]bool{}, nil)

	channelPoint := newTestOutPoint()
	client.On("ListChannels").Return([]*lightning.Channel{
		{PeerId: destination, ChannelPoint: channelPoint},
	}, nil)

	store.On("ConsumeLsps2Registration", uint64(12345), channelPoint, paymentSize, uint64(1000)).Return(true, nil)

	i := &Interceptor{store: store, client: client}
	i.RecoverStuckLsps2Registrations()

	store.AssertCalled(t, "ConsumeLsps2Registration", uint64(12345), channelPoint, paymentSize, uint64(1000))
	store.AssertNotCalled(t, "ReleaseLsps2Registration", mock.Anything)
}

// TestRecoverStuckLsps2Registrations_NoChannel_ReleasesForRetry covers the
// other case: no matching channel exists (the crash happened before
// openChannel ever succeeded, or it genuinely failed) -- safe to release
// back to ACTIVE so a real HTLC can retry the registration.
func TestRecoverStuckLsps2Registrations_NoChannel_ReleasesForRetry(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)

	destination := fakePubkey(0x12)
	stuck := &core.Lsps2BuyRegistration{
		Id:     "reg-stuck-2",
		PeerId: hexEncode(destination),
		Scid:   54321,
		State:  core.Lsps2RegistrationOpening,
	}
	store.On("GetStuckLsps2Registrations").Return([]*core.Lsps2BuyRegistration{stuck}, nil)
	client.On("ListChannels").Return([]*lightning.Channel{}, nil) // no channels at all
	store.On("GetClaimedLsps2ChannelPoints").Return(map[string]bool{}, nil)
	store.On("ReleaseLsps2Registration", uint64(54321)).Return(nil)

	i := &Interceptor{store: store, client: client}
	i.RecoverStuckLsps2Registrations()

	store.AssertCalled(t, "ReleaseLsps2Registration", uint64(54321))
	store.AssertNotCalled(t, "ConsumeLsps2Registration", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestRecoverStuckLsps2Registrations_NoneStuck_NoOp covers the common case
// (nothing to recover) doesn't call ListChannels unnecessarily.
func TestRecoverStuckLsps2Registrations_NoneStuck_NoOp(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	store.On("GetStuckLsps2Registrations").Return([]*core.Lsps2BuyRegistration{}, nil)

	i := &Interceptor{store: store, client: client}
	i.RecoverStuckLsps2Registrations()

	client.AssertNotCalled(t, "ListChannels")
}

// TestRecoverStuckLsps2Registrations_VariableSizeMode_UsesMaxPaymentSizeNotInflatedReservedAmount
// is the regression test for the accounting-overstatement finding: in
// variable-size mode (PaymentSizeMloki nil), the crash-recovery fallback
// used to record ReservedAmountLoki*1000 as the payment size -- but
// ReservedAmountLoki also bakes in capacity padding and the BOLT2 reserve
// top-up, systematically overstating the real payment. It must instead
// prefer the registration's own MaxPaymentSizeMloki (F-22's worst-case
// bound), a much closer approximation of the real redeemed amount.
func TestRecoverStuckLsps2Registrations_VariableSizeMode_UsesMaxPaymentSizeNotInflatedReservedAmount(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)

	destination := fakePubkey(0x16)
	maxPaymentSize := uint64(21_000_000) // real worst-case payment bound
	stuck := &core.Lsps2BuyRegistration{
		Id:                  "reg-stuck-6",
		PeerId:              hexEncode(destination),
		Scid:                99999,
		PaymentSizeMloki:    nil, // variable-size mode
		MaxPaymentSizeMloki: &maxPaymentSize,
		ParamsMinFeeMloki:   1000,
		ReservedAmountLoki:  30_000, // inflated by padding/BOLT2 top-up -- must NOT be used
		State:               core.Lsps2RegistrationOpening,
	}
	store.On("GetStuckLsps2Registrations").Return([]*core.Lsps2BuyRegistration{stuck}, nil)

	channelPoint := newTestOutPoint()
	client.On("ListChannels").Return([]*lightning.Channel{
		{PeerId: destination, ChannelPoint: channelPoint},
	}, nil)
	store.On("GetClaimedLsps2ChannelPoints").Return(map[string]bool{}, nil)

	store.On("ConsumeLsps2Registration", uint64(99999), channelPoint, maxPaymentSize, uint64(1000)).Return(true, nil)

	i := &Interceptor{store: store, client: client}
	i.RecoverStuckLsps2Registrations()

	store.AssertCalled(t, "ConsumeLsps2Registration", uint64(99999), channelPoint, maxPaymentSize, uint64(1000))
	store.AssertNotCalled(t, "ConsumeLsps2Registration", uint64(99999), channelPoint, uint64(30_000_000), mock.Anything)
}

// TestRecoverStuckLsps2Registrations_PreExistingChannelAlreadyClaimed_NotMisattributed
// is the regression test for the crash-recovery peer-matching bug: a peer
// with a pre-existing channel (from an earlier order, already recorded in
// lsps2.bought_channels) must NOT have that unrelated channel wrongly
// attributed to a different, later registration stuck at OPENING. Before
// this fix, RecoverStuckLsps2Registrations matched on peer ID alone with
// no exclusion of already-claimed channels, so this exact case would have
// falsely marked the stuck registration CONSUMED against someone else's
// channel.
func TestRecoverStuckLsps2Registrations_PreExistingChannelAlreadyClaimed_NotMisattributed(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)

	destination := fakePubkey(0x14)
	stuck := &core.Lsps2BuyRegistration{
		Id:     "reg-stuck-4",
		PeerId: hexEncode(destination),
		Scid:   77777,
		State:  core.Lsps2RegistrationOpening,
	}
	store.On("GetStuckLsps2Registrations").Return([]*core.Lsps2BuyRegistration{stuck}, nil)

	// This peer already has one channel, but it's already recorded as the
	// funding for a completed, unrelated purchase.
	preExisting := newTestOutPoint()
	client.On("ListChannels").Return([]*lightning.Channel{
		{PeerId: destination, ChannelPoint: preExisting},
	}, nil)
	store.On("GetClaimedLsps2ChannelPoints").Return(map[string]bool{preExisting.String(): true}, nil)
	store.On("ReleaseLsps2Registration", uint64(77777)).Return(nil)

	i := &Interceptor{store: store, client: client}
	i.RecoverStuckLsps2Registrations()

	// No unclaimed candidate remains (the only channel is already
	// claimed), so this must fall through to release-for-retry, NOT
	// consume against the unrelated, already-claimed channel.
	store.AssertNotCalled(t, "ConsumeLsps2Registration", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertCalled(t, "ReleaseLsps2Registration", uint64(77777))
}

// TestRecoverStuckLsps2Registrations_MultipleUnclaimedChannels_Ambiguous_LeavesStuck
// covers the case a peer has more than one unclaimed channel: recovery
// must NOT guess which one (if any) belongs to this registration --
// neither consuming nor releasing it, leaving it stuck at OPENING for
// manual review, mirroring findUnclaimedChannelPoint's own
// zero-or-many-is-ambiguous rule.
func TestRecoverStuckLsps2Registrations_MultipleUnclaimedChannels_Ambiguous_LeavesStuck(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)

	destination := fakePubkey(0x15)
	stuck := &core.Lsps2BuyRegistration{
		Id:     "reg-stuck-5",
		PeerId: hexEncode(destination),
		Scid:   88888,
		State:  core.Lsps2RegistrationOpening,
	}
	store.On("GetStuckLsps2Registrations").Return([]*core.Lsps2BuyRegistration{stuck}, nil)

	channelA := newTestOutPoint()
	channelB := &wire.OutPoint{Index: 1} // distinct from channelA's zero-value outpoint
	client.On("ListChannels").Return([]*lightning.Channel{
		{PeerId: destination, ChannelPoint: channelA},
		{PeerId: destination, ChannelPoint: channelB},
	}, nil)
	store.On("GetClaimedLsps2ChannelPoints").Return(map[string]bool{}, nil)

	i := &Interceptor{store: store, client: client}
	i.RecoverStuckLsps2Registrations()

	store.AssertNotCalled(t, "ConsumeLsps2Registration", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "ReleaseLsps2Registration", mock.Anything)
}

// TestRecoverStuckLsps2Registrations_ListChannelsFails_DefersToNextPass
// proves a failed ListChannels call doesn't release (or consume) any
// registration on incomplete information -- safer to defer to the next
// periodic pass than guess.
func TestRecoverStuckLsps2Registrations_ListChannelsFails_DefersToNextPass(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)

	stuck := &core.Lsps2BuyRegistration{Id: "reg-stuck-3", PeerId: hexEncode(fakePubkey(0x13)), Scid: 111}
	store.On("GetStuckLsps2Registrations").Return([]*core.Lsps2BuyRegistration{stuck}, nil)
	client.On("ListChannels").Return(nil, assert.AnError)

	i := &Interceptor{store: store, client: client}
	i.RecoverStuckLsps2Registrations()

	store.AssertNotCalled(t, "ReleaseLsps2Registration", mock.Anything)
	store.AssertNotCalled(t, "ConsumeLsps2Registration", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}
