package notifications

import (
	"context"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
)

type Store interface {
	Register(ctx context.Context, pubkey string, url string, transport string) error
	GetRegistrations(ctx context.Context, pubkey string) ([]core.NotificationRegistration, error)
	Unsubscribe(ctx context.Context, pubkey string, url string) error
	RemoveExpired(ctx context.Context, before time.Time) error

	// Queue management
	Enqueue(ctx context.Context, pubkey string, url string, transport string, method string, params interface{}) (int64, error)
	GetPending(ctx context.Context, limit int) ([]NotificationQueueItem, error)
	UpdateStatus(ctx context.Context, id int64, status string, nextRetry time.Time) error
}

type NotificationQueueItem struct {
	ID        int64
	Pubkey    string
	TargetURL string
	Transport string
	Method    string
	Params    interface{}
	Attempts  int
}
