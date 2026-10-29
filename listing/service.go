// Package listing lists a Hub's offers on relays (NIP-CASH §Listing Offers,
// §Offer Listing, §Hub Rules): kind-31190 versions signed by the node identity,
// kept fresh while the offer is served, and retired in the spec's order. A
// listing advertises; the terms are always get_info's.
//
// What is listed comes from the Hub's own rows (an Order connection, or a
// circle's own listing connection), never from a pasted string, and never a
// Cash Hub connection. lokihub never builds or signs the operator's kind 11191:
// it names the operator in the listing's p tag and checks, read-only, whether
// the operator's newest 11191 names the listing.
package listing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipCO"
	nmlisting "github.com/ohstr/nmilat/nipcash/listing"
)

// Timings (NIP-CASH §Hub Rules; plan OQ-46).
const (
	// ExpirationSecs is how far ahead a version's expiration is set: the maximum.
	ExpirationSecs = nmlisting.MaxExpirySecs
	// RepublishSecs is how often a listed offer is republished.
	RepublishSecs = 7 * 24 * 3600
	// OperatorCheckSecs is how often the operator line is checked.
	OperatorCheckSecs = 6 * 3600
	// retryFirst and retryMax bound the publish backoff; alertAfter is how long a
	// version may go unaccepted before an ERROR.
	retryFirst = 30
	retryMax   = 600
	alertAfter = 3600
	// leaseSecs is how long a scheduler pass holds a row it is working on, so a
	// second instance on the same database leaves it alone.
	leaseSecs = 120
	// tickInterval is how often the scheduler looks for due rows.
	tickInterval = 15 * time.Second
	// maxWaitForClock is the longest an admin call waits for the clock to pass
	// the previous version's created_at before handing the publish to the
	// scheduler.
	maxWaitForClock = 2 * time.Second
)

// Refusals the admin API maps to HTTP statuses.
var (
	// ErrRefused is a target or field the API refuses (400).
	ErrRefused = errors.New("refused")
	// ErrNotFound is no such listing (404).
	ErrNotFound = errors.New("listing not found")
	// ErrCannotSign is a node that cannot sign the listing or its string (503).
	ErrCannotSign = errors.New("the node cannot sign")
)

func refused(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, a...))
}

// NodeSigner signs events as the node identity.
type NodeSigner interface {
	// Identity is the node identity, x-only, 64 lowercase hex.
	Identity(ctx context.Context) (string, error)
	// Sign sets ev's id and signature; ev.PubKey is the node identity.
	Sign(ctx context.Context, ev *nip01.Event) error
	// Network is what get_info reports as network.
	Network(ctx context.Context) (string, error)
}

// Relays publishes events and reads them back.
type Relays interface {
	// Publish sends ev to each url and returns each relay's answer (nil: OK true).
	Publish(ctx context.Context, urls []string, ev *nip01.Event) map[string]error
	// Fetch returns the events matching f on urls, deduplicated by id.
	Fetch(ctx context.Context, urls []string, f nip01.SubscriptionFilter) ([]*nip01.Event, error)
}

// Settings is the part of config.Config listings read.
type Settings interface {
	GetRelayUrls() []string
	GetGeneralRelayUrls() []string
	Get(key string, encryptionKey string) (string, error)
	SetUpdate(key string, value string, encryptionKey string) error
}

// PairingKeys derives a connection secret from an app id.
type PairingKeys interface {
	GetCashPairingKey(appID uint) (string, error)
}

// CircleStringSigner issues a signed circlehub1 string (connstr.CircleHub).
type CircleStringSigner func(ctx context.Context, pubkey string, relays []string, secret, label string) (string, error)

// OrderOffer is what a cash_order listing lists, from the Order connection's own
// rows (WP-21).
type OrderOffer struct {
	// Conn is the signed Order connection, issued from the app row.
	Conn *nipCO.OrderConnection
	// HubAppID is the Cash Hub the Order connection sells for.
	HubAppID uint
	// Retired is an Order connection whose get_info answers "cash_order": {}.
	Retired bool
	// Summary holds lightning and/or onchain, from the kinds sold.
	Summary []string
	// Hints are the current terms, one per payment kind sold.
	Hints []nmlisting.Hint
}

// OrderSource resolves Order connections (WP-21; cashorder.ListingSource). Nil
// refuses every cash_order listing. The retire endpoint calls RetireOrder after
// the retire commit.
type OrderSource interface {
	Order(ctx context.Context, connAppID uint) (*OrderOffer, error)
}

// Deps are what the listing service needs from the rest of lokihub.
type Deps struct {
	DB           *gorm.DB
	Settings     Settings
	Keys         PairingKeys
	Signer       NodeSigner
	CircleString CircleStringSigner
	Relays       Relays
	// Orders is optional (see OrderSource).
	Orders OrderSource
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// InvalidateGetInfo drops a connection's cached get_info (OQ-47), called
	// whenever a listing's connection changes.
	InvalidateGetInfo func(appID uint)
}

// Service lists offers.
type Service struct {
	d Deps
}

// New returns the listing service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.InvalidateGetInfo == nil {
		d.InvalidateGetInfo = func(uint) {}
	}
	return &Service{d: d}
}

func (s *Service) now() int64 { return s.d.Now().Unix() }
