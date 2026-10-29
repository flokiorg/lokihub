package cashrequest_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/db"
)

// NIP-CASH §Lost Replies: "unknown" means the call never commits. A commit that
// passed its deadline check at the deadline and is still open when a lookup
// reads after the deadline is waited for, so the lookup reads its record.
func TestGetSettled_WaitsForACommitInFlight(t *testing.T) {
	svc, s, now := newStore(t)
	id := rid(40)
	written, release := make(chan struct{}), make(chan struct{})
	committed := make(chan error, 1)
	go func() {
		committed <- svc.DB.Transaction(func(tx *gorm.DB) error {
			if err := s.CommitTx(tx, record(id, now.Unix()), nil); err != nil {
				return err
			}
			close(written)
			<-release
			return nil
		})
	}()
	<-written

	got := make(chan *db.CashRequestRecord, 1)
	go func() {
		rec, err := s.GetSettled(id)
		assert.NoError(t, err)
		got <- rec
	}()
	select {
	case <-got:
		t.Fatal("the settled read did not wait for the open commit")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-committed)
	rec := <-got
	require.NotNil(t, rec, "the lookup reads the record the commit wrote")
	assert.Equal(t, id, rec.RequestID)
}

func TestGetSettled_NoCommitReadsNothing(t *testing.T) {
	_, s, _ := newStore(t)
	rec, err := s.GetSettled(rid(41))
	require.NoError(t, err)
	assert.Nil(t, rec)
}

// The other side: once a lookup judged the deadline passed, a commit checking
// after it is refused; one checking exactly at the deadline commits.
func TestCommitTx_AfterALookupPastTheDeadlineIsRefused(t *testing.T) {
	svc, s, now := newStore(t)
	deadline := now.Unix()
	rec, err := s.GetSettled(rid(42))
	require.NoError(t, err)
	require.Nil(t, rec)

	late := cashrequest.Store{DB: svc.DB, Now: func() time.Time { return time.Unix(deadline+1, 0) }}
	err = svc.DB.Transaction(func(tx *gorm.DB) error { return late.CommitTx(tx, record(rid(42), deadline), nil) })
	require.ErrorIs(t, err, cashrequest.ErrDeadlinePassed)
	rec, err = s.GetSettled(rid(42))
	require.NoError(t, err)
	assert.Nil(t, rec, "nothing committed after the deadline")

	atDeadline := cashrequest.Store{DB: svc.DB, Now: func() time.Time { return time.Unix(deadline, 0) }}
	require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error { return atDeadline.CommitTx(tx, record(rid(43), deadline), nil) }))
	rec, err = s.GetSettled(rid(43))
	require.NoError(t, err)
	assert.NotNil(t, rec)
}
