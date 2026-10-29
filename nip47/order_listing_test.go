package nip47

import (
	"context"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	nmlisting "github.com/ohstr/nmilat/nipcash/listing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/db"
)

// The listing service's OrderSource: the signed connection, its Cash Hub, the
// kinds sold and their terms as hints; retired, no kind.
func TestOrderListingSource(t *testing.T) {
	f := newOrderFixture(t)
	src := cashorder.ListingSource{Deps: func() cashorder.Deps {
		return cashorder.Deps{DB: f.svc.DB, Keys: f.svc.Keys, LN: f.ln, Relays: []string{"wss://relay.example.com"}}
	}}
	offer, err := src.Order(context.TODO(), f.conn.AppID)
	require.NoError(t, err)
	require.NotNil(t, offer)
	require.NoError(t, offer.Conn.Verify())
	assert.Equal(t, f.conn.WalletPubkey, offer.Conn.Pubkey)
	assert.Equal(t, f.hub.ID, offer.HubAppID)
	assert.False(t, offer.Retired)
	terms := orderTermsFixture().Terms
	var want []string
	if terms.Lightning != nil {
		want = append(want, nmlisting.SummaryLightning)
	}
	if terms.Onchain != nil {
		want = append(want, nmlisting.SummaryOnchain)
	}
	assert.Equal(t, want, offer.Summary)
	require.Len(t, offer.Hints, len(want))
	assert.Equal(t, terms.Lightning.MaxMillis, offer.Hints[0].MaxMillis)

	none, err := src.Order(context.TODO(), f.hub.ID)
	require.NoError(t, err)
	assert.Nil(t, none, "a Cash Hub is no Order connection")

	require.NoError(t, cashorder.RetireConnection(cashorder.Deps{DB: f.svc.DB}, f.hub.ID, f.conn.AppID, time.Now()))
	offer, err = src.Order(context.TODO(), f.conn.AppID)
	require.NoError(t, err)
	assert.True(t, offer.Retired)
	assert.Empty(t, offer.Summary)
}

// U-25.10 (Order half): every get_info on an Order connection is answered, from
// a result at most 5 s old; new terms and the retirement drop it at once.
func TestOrderGetInfoCached(t *testing.T) {
	f := newOrderFixture(t)
	for range 200 {
		require.NotEmpty(t, f.getInfo(t)["cash_order"])
	}
	// A change behind the Hub's back is served from the cache...
	require.NoError(t, f.svc.DB.Model(&db.CashOrderTerms{}).Where("hub_app_id = ?", f.hub.ID).
		Update("terms_json", `{"bill":{}}`).Error)
	assert.Contains(t, string(f.getInfo(t)["cash_order"]), "lightning")
	// ...new terms through the Hub are not.
	s := orderTermsFixture()
	s.Terms.Onchain = nil
	require.NoError(t, cashorder.PutSettings(f.svc.DB, f.hub.ID, s, time.Now()))
	got, err := nipCO.ParseOrderInfo(f.getInfo(t)["cash_order"])
	require.NoError(t, err)
	assert.Nil(t, got.Onchain)
	require.NoError(t, cashorder.RetireConnection(cashorder.Deps{DB: f.svc.DB}, f.hub.ID, f.conn.AppID, time.Now()))
	assert.JSONEq(t, `{}`, string(f.getInfo(t)["cash_order"]), "retired: answered at once")
}
