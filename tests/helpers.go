package tests

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// RandomHex32 returns a random 64-character lowercase hex string (32 random bytes).
func RandomHex32() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// FundApp inserts a settled incoming transaction so the app has a balance.
// paymentHash must be unique within a test to avoid DB UNIQUE constraint errors.
func FundApp(svc *TestService, appID uint, amountMloki uint64, paymentHash string) {
	svc.DB.Create(&db.Transaction{
		AppId:       &appID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: amountMloki,
		PaymentHash: paymentHash,
	})
}
