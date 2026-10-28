package listing

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/ohstr/nmilat/nip01"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// ListingRelaysKey is the setting naming the listing relays (OQ-44): a comma
// list, the general relays when empty.
const ListingRelaysKey = "CashListingRelays"

// kindRelayList is NIP-65's relay list.
const kindRelayList = 10002

// ListingRelays are the relays listings are published to and the node's kind
// 10002 names as write relays: the setting, else the general relays, else the
// NWC relays.
func (s *Service) ListingRelays() []string {
	if v, _ := s.d.Settings.Get(ListingRelaysKey, ""); strings.TrimSpace(v) != "" {
		return splitRelays(v)
	}
	if general := cleanRelays(s.d.Settings.GetGeneralRelayUrls()); len(general) > 0 {
		return general
	}
	return cleanRelays(s.d.Settings.GetRelayUrls())
}

// ConfiguredListingRelays is the setting as stored (empty: the default).
func (s *Service) ConfiguredListingRelays() []string {
	v, _ := s.d.Settings.Get(ListingRelaysKey, "")
	return splitRelays(v)
}

// SetListingRelays stores the setting and republishes the 10002 and every listed
// offer, since their targets changed.
func (s *Service) SetListingRelays(ctx context.Context, relays []string) error {
	clean := cleanRelays(relays)
	for _, r := range clean {
		if !strings.HasPrefix(r, "wss://") && !strings.HasPrefix(r, "ws://") {
			return refused("relay %q is not a websocket URL", r)
		}
	}
	if err := s.d.Settings.SetUpdate(ListingRelaysKey, strings.Join(clean, ","), ""); err != nil {
		return err
	}
	if err := s.ensureRelayList(ctx); err != nil {
		logger.Logger.Warn().Err(err).Msg("Could not publish the node's relay list")
	}
	return s.d.DB.Model(&db.OfferListing{}).Where("phase = ?", db.ListingPhaseListed).
		Update("due_at", 0).Error
}

func splitRelays(v string) []string {
	return cleanRelays(strings.Split(v, ","))
}

// maxOperatorRelays bounds the write relays taken from an operator's list.
const maxOperatorRelays = 8

// publicRelay: a wss:// URL whose host is a name or a public address, never
// localhost, a loopback, private, link-local or unspecified address.
func publicRelay(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "wss" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast())
	}
	return true
}

func cleanRelays(in []string) []string {
	var out []string
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func union(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return cleanRelays(out)
}

// ensureRelayList publishes the node identity's NIP-65 kind 10002 naming the
// listing relays as write relays, when it has not published that list yet
// (NIP-CASH §Hub Rules). created_at is never post-dated and always after the
// previous one.
func (s *Service) ensureRelayList(ctx context.Context) error {
	relays := s.ListingRelays()
	if len(relays) == 0 {
		return fmt.Errorf("no listing relays")
	}
	encoded, _ := json.Marshal(relays)
	var st db.ListingNodeState
	if err := s.d.DB.Where(db.ListingNodeState{ID: 1}).FirstOrCreate(&st).Error; err != nil {
		return err
	}
	if st.RelayListRelays == string(encoded) {
		return nil
	}
	createdAt := s.now()
	if createdAt <= st.RelayListCreatedAt {
		createdAt = st.RelayListCreatedAt + 1
		if createdAt > s.now() {
			return fmt.Errorf("the clock is not past the last relay list yet")
		}
	}
	identity, err := s.d.Signer.Identity(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCannotSign, err)
	}
	ev := RelayListEvent(identity, relays, createdAt)
	if err := s.d.Signer.Sign(ctx, ev); err != nil {
		return fmt.Errorf("%w: %v", ErrCannotSign, err)
	}
	answers := s.d.Relays.Publish(ctx, union(relays, s.d.Settings.GetGeneralRelayUrls()), ev)
	if accepted(answers) == 0 {
		return fmt.Errorf("no relay accepted the relay list")
	}
	return s.d.DB.Model(&db.ListingNodeState{}).Where("id = ?", 1).
		Updates(map[string]any{"relay_list_relays": string(encoded), "relay_list_created_at": createdAt}).Error
}

// RelayListEvent is the node's unsigned kind 10002: one ["r", url, "write"] per
// listing relay.
func RelayListEvent(identity string, relays []string, createdAt int64) *nip01.Event {
	tags := make([][]string, 0, len(relays))
	for _, r := range relays {
		tags = append(tags, []string{"r", r, "write"})
	}
	return &nip01.Event{Kind: kindRelayList, PubKey: identity, CreatedAt: uint64(createdAt), Tags: tags} //nolint:gosec // a unix time
}

// writeRelaysOf reads pubkey's newest NIP-65 list from relays and returns its
// write relays (an unmarked r is read and write).
func (s *Service) writeRelaysOf(ctx context.Context, pubkey string, relays []string) []string {
	if len(relays) == 0 {
		return nil
	}
	evs, err := s.d.Relays.Fetch(ctx, relays, nip01.SubscriptionFilter{
		Authors: []string{pubkey}, Kinds: []int{kindRelayList}, Limit: 5,
	})
	if err != nil {
		return nil
	}
	var newest *nip01.Event
	for _, ev := range evs {
		if ev.PubKey != pubkey || ev.Kind != kindRelayList {
			continue
		}
		if newest == nil || nip01.Supersedes(ev, newest) {
			newest = ev
		}
	}
	if newest == nil {
		return nil
	}
	var out []string
	for _, t := range newest.Tags {
		if len(t) < 2 || t[0] != "r" {
			continue
		}
		if len(t) >= 3 && t[2] != "write" {
			continue
		}
		// A third party's list: only public wss relays, and a few (R2-L5).
		if !publicRelay(t[1]) {
			continue
		}
		out = append(out, t[1])
	}
	out = cleanRelays(out)
	if len(out) > maxOperatorRelays {
		out = out[:maxOperatorRelays]
	}
	return out
}

func accepted(answers map[string]error) int {
	n := 0
	for _, err := range answers {
		if err == nil {
			n++
		}
	}
	return n
}

// lockRow re-reads a listing row for update inside tx (FOR UPDATE on postgres;
// sqlite serializes writers by itself).
func lockRow(tx *gorm.DB, id uint) (*db.OfferListing, error) {
	var row db.OfferListing
	q := tx
	if tx.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}
