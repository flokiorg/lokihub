package listing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ohstr/nmilat/nip01"
	nmlisting "github.com/ohstr/nmilat/nipcash/listing"

	"github.com/flokiorg/lokihub/db"
)

// View is a listing as the admin API shows it, with its health line.
type View struct {
	ID        uint   `json:"id"`
	Type      string `json:"type"`
	HubAppID  uint   `json:"hubAppId"`
	ConnAppID uint   `json:"connAppId"`
	D         string `json:"d"`
	// Address is 31190:<node identity>:<d>; AddressRelay the relay hint to copy
	// with it, and ATag the ready ["a", address, relay] line for the operator's
	// own Nostr client.
	Address      string   `json:"address"`
	AddressRelay string   `json:"addressRelay"`
	ATag         []string `json:"aTag"`
	// OperatorCommand is the cashctl command an operator runs to name the offer
	// (XQ-42); lokihub never publishes the operator's 11191.
	OperatorCommand string `json:"operatorCommand"`

	Title          string   `json:"title"`
	Content        string   `json:"content"`
	Image          string   `json:"image"`
	Location       string   `json:"location"`
	Languages      []string `json:"languages"`
	Geohashes      []string `json:"geohashes"`
	Hints          bool     `json:"hints"`
	FollowSetA     bool     `json:"followSetA"`
	OperatorPubkey string   `json:"operatorPubkey"`
	OperatorRelay  string   `json:"operatorRelay"`

	Status string `json:"status"`
	Phase  string `json:"phase"`
	// Connection is the listed string, public (a circle's listing connection, or
	// the Order connection).
	Connection        string `json:"connection"`
	LastCreatedAt     int64  `json:"lastCreatedAt"`
	LastEventID       string `json:"lastEventId"`
	LastOpenExpiresAt *int64 `json:"lastOpenExpiresAt"`
	RelaysOK          int    `json:"relaysOk"`
	RelaysTotal       int    `json:"relaysTotal"`
	Failing           bool   `json:"failing"`
	OperatorVerified  *bool  `json:"operatorVerified"`
	OperatorCheckedAt *int64 `json:"operatorCheckedAt"`
	ClosedAt          *int64 `json:"closedAt"`
	DeleteDueAt       *int64 `json:"deleteDueAt"`
	DeletedAt         *int64 `json:"deletedAt"`
}

// List returns every listing, newest first.
func (s *Service) List(ctx context.Context) ([]View, error) {
	var rows []db.OfferListing
	if err := s.d.DB.Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	identity, _ := s.d.Signer.Identity(ctx)
	out := make([]View, 0, len(rows))
	for i := range rows {
		out = append(out, s.view(&rows[i], identity))
	}
	return out, nil
}

// Get returns one listing.
func (s *Service) Get(ctx context.Context, id uint) (*View, error) {
	row, err := s.load(id)
	if err != nil {
		return nil, err
	}
	identity, _ := s.d.Signer.Identity(ctx)
	v := s.view(row, identity)
	return &v, nil
}

func (s *Service) view(row *db.OfferListing, identity string) View {
	v := View{
		ID: row.ID, Type: row.Type, HubAppID: row.HubAppID, ConnAppID: row.ConnAppID, D: row.D,
		Title: row.Title, Content: row.Content, Image: row.Image, Location: row.Location,
		Languages: decodeList(row.Languages), Geohashes: decodeList(row.Geohashes),
		Hints: row.Hints, FollowSetA: row.FollowSetA,
		OperatorPubkey: ptr(row.OperatorPubkey), OperatorRelay: ptr(row.OperatorRelay),
		Status: row.Status, Phase: row.Phase,
		LastCreatedAt: row.LastCreatedAt, LastEventID: row.LastEventID, LastOpenExpiresAt: row.LastOpenExpiresAt,
		RelaysOK: row.RelaysOK, RelaysTotal: row.RelaysTotal, Failing: row.FailingSince != nil,
		OperatorVerified: row.OperatorVerified, OperatorCheckedAt: row.OperatorCheckedAt,
		ClosedAt: row.ClosedAt, DeleteDueAt: row.DeleteDueAt, DeletedAt: row.DeletedAt,
	}
	if identity != "" {
		v.Address = nmlisting.Address(identity, row.D)
		if l := s.ListingRelays(); len(l) > 0 {
			v.AddressRelay = l[0]
		}
		v.ATag = []string{"a", v.Address}
		if v.AddressRelay != "" {
			v.ATag = append(v.ATag, v.AddressRelay)
		}
		v.OperatorCommand = "cash discover operator add " + v.Address
	}
	if row.LastEventJSON != "" {
		var ev nip01.Event
		if json.Unmarshal([]byte(row.LastEventJSON), &ev) == nil {
			for _, t := range ev.Tags {
				if len(t) >= 2 && t[0] == "connection" {
					v.Connection = t[1]
				}
			}
		}
	}
	return v
}

// errPreview stops NewListing after it handed the built event to the signer.
var errPreview = errors.New("preview")

// Preview is the unsigned event the next publish would sign (no id, no
// signature).
func (s *Service) Preview(ctx context.Context, id uint) (*nip01.Event, error) {
	row, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if row.Phase != db.ListingPhaseListed {
		return nil, refused("only a listed offer has a next version")
	}
	fields, _, err := s.build(ctx, row)
	if err != nil {
		return nil, err
	}
	createdAt := max(s.now(), row.LastCreatedAt+1)
	fields.Expiration = createdAt + ExpirationSecs
	var captured *nip01.Event
	_, err = nmlisting.NewListing(fields, createdAt, createdAt, func(ev *nip01.Event) error {
		captured = ev.Copy()
		return errPreview
	})
	if captured == nil {
		if err == nil {
			err = errors.New("nothing built")
		}
		return nil, refused("%v", err)
	}
	captured.ID, captured.Sig = "", ""
	return captured, nil
}

// CheckOperator checks the operator line now (WP-25 item 9): the operator's
// newest 11191, from their write relays and the listing relays, names this
// listing's address. Read-only: nothing is signed or published.
func (s *Service) CheckOperator(ctx context.Context, id uint) (*View, error) {
	row, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if row.OperatorPubkey == nil {
		return nil, refused("this listing names no operator")
	}
	if err := s.checkOperator(ctx, row); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) checkOperator(ctx context.Context, row *db.OfferListing) error {
	identity, err := s.d.Signer.Identity(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCannotSign, err)
	}
	operator := *row.OperatorPubkey
	relays := union(s.writeRelaysOf(ctx, operator, s.lookupRelays()), s.lookupRelays(), []string{ptr(row.OperatorRelay)})
	evs, err := s.d.Relays.Fetch(ctx, relays, nip01.SubscriptionFilter{
		Authors: []string{operator}, Kinds: []int{nmlisting.KindOperatorOffers}, Limit: 10,
	})
	if err != nil {
		return err
	}
	newest := nmlisting.NewestOperatorOffers(evs, operator)
	l := &nmlisting.Listing{NodeIdentity: identity, D: row.D, Operator: &nmlisting.Ref{Pubkey: operator}}
	verified := nmlisting.OperatorVerified(l, newest)
	now := s.now()
	return s.d.DB.Model(&db.OfferListing{}).Where("id = ?", row.ID).
		Updates(map[string]any{"operator_verified": verified, "operator_checked_at": now}).Error
}
