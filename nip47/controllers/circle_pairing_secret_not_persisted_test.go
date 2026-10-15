package controllers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Pairing Connection: a Circle Wallet MUST keep a one-time-random
// pairing secret, never persisted after generation (CW-pairing-connection-01).
// The member receives it once, inside encrypted_details; afterwards it must
// exist nowhere in the Hub's database, in any table or column.
func TestCreateCircleWallet_PairingSecretNeverPersisted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	if svc.DB.Name() != "sqlite" {
		t.Skip("the table scan below reads sqlite_master")
	}

	provider := createCircleHub(t, svc, 7200, 1_000_000)
	member := nostr.GeneratePrivateKey()
	response := joinCircle(t, context.TODO(), svc, provider, member)
	require.Nil(t, response.Error, "%+v", response.Error)

	details := decryptCircleWalletDetails(t, member, *provider.WalletPubkey,
		response.Result.(createCircleWalletResponse).EncryptedDetails)
	pairing, err := url.Parse(details.PairingURI)
	require.NoError(t, err)
	secret := pairing.Query().Get("secret")
	require.Len(t, secret, 64, "the member's pairing URI carries the secret")

	var tables []string
	require.NoError(t, svc.DB.Raw("SELECT name FROM sqlite_master WHERE type = 'table'").Scan(&tables).Error)
	require.NotEmpty(t, tables)
	for _, table := range tables {
		rows, err := svc.DB.Raw(fmt.Sprintf("SELECT * FROM %q", table)).Rows()
		require.NoError(t, err)
		columns, err := rows.Columns()
		require.NoError(t, err)
		for rows.Next() {
			values := make([]interface{}, len(columns))
			pointers := make([]interface{}, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			require.NoError(t, rows.Scan(pointers...))
			for i, value := range values {
				var text string
				switch v := value.(type) {
				case string:
					text = v
				case []byte:
					text = string(v)
				}
				require.False(t, strings.Contains(strings.ToLower(text), secret),
					"the pairing secret is stored in %s.%s", table, columns[i])
			}
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
}
