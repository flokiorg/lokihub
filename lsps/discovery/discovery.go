// Package discovery builds lokihub's LSP catalogue from NIP-LSP
// announcements (kind 10735) found on relays (WP-26). It replaces the
// services.json LSP list (LP-2): the user ticks the LSPs they trust (LP-1,
// LP-12), and nothing connects to an LSP the user has not trusted.
//
// Each node's newest announcement is kept, and the newest seen is remembered
// even when it fails a check, so an older one is never used again (NIP-LSP
// §Client Behavior). The announcement only advertises: the live LSPS answers
// decide (LiveCheck).
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/niplsp"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/nodesign"
)

// MaxAnnouncements bounds how many announcements are read per pass and how many
// untrusted LSPs the catalogue keeps (NIP-LSP §Security Considerations, spam;
// XQ-49).
const MaxAnnouncements = 200

// MaxFutureSecs is how far ahead of this node's clock an announcement's
// created_at may lie (NIP-LSP §Client Behavior step 4): one dated further
// ahead is ignored and does not count as seen.
const MaxFutureSecs = 15 * 60

// RefreshInterval is how often the catalogue is re-read from relays.
const RefreshInterval = 30 * time.Minute

// ErrNoAnnouncement is a node with no usable announcement on the relays: a
// manual add is refused then (LP-4).
var ErrNoAnnouncement = errors.New("no usable LSP announcement found for this node")

// Fetcher reads events from relays.
type Fetcher interface {
	Fetch(ctx context.Context, relays []string, f nip01.SubscriptionFilter) ([]*nip01.Event, error)
}

// GraphFact is what this node's own view of the network knows about a node id.
type GraphFact struct {
	Present  bool
	Channels int
	Capacity int64
}

// Graph reads facts about node ids from this node's own graph.
type Graph interface {
	Facts(ctx context.Context, nodeIDs []string) (map[string]GraphFact, error)
}

// Settings are the relays the catalogue is read from.
type Settings interface {
	GetRelayUrls() []string
	GetGeneralRelayUrls() []string
}

// Deps are what the catalogue needs.
type Deps struct {
	DB       *gorm.DB
	Settings Settings
	Fetcher  Fetcher
	Graph    Graph
	// Network is the node's network as get_info names it (mainnet, regtest…).
	Network func(ctx context.Context) (string, error)
	// Live runs the LSPS side of the live check; nil until the liquidity
	// manager runs.
	Live LSPSClient
	Now  func() time.Time
	// Chain is the chain name this node uses; nodesign.Chain when empty.
	Chain string
}

// Service is the LSP catalogue.
type Service struct {
	d Deps
}

// New returns the catalogue service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Chain == "" {
		d.Chain = nodesign.Chain
	}
	return &Service{d: d}
}

// Run refreshes the catalogue now and every RefreshInterval until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(RefreshInterval)
	defer t.Stop()
	for {
		if err := s.Discover(ctx); err != nil {
			logger.Logger.Warn().Err(err).Msg("LSP catalogue refresh failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// readRelays are where announcements are read: the configured relays and every
// trusted LSP's own r relays (XQ-49).
func (s *Service) readRelays() []string {
	relays := append(slices.Clone(s.d.Settings.GetGeneralRelayUrls()), s.d.Settings.GetRelayUrls()...)
	var trusted []persist.LSP
	_ = s.d.DB.Where("trusted = ?", true).Find(&trusted).Error
	for _, l := range trusted {
		relays = append(relays, decodeList(l.Relays)...)
	}
	return clean(relays)
}

// Discover reads the newest announcements from the relays and updates the
// catalogue.
func (s *Service) Discover(ctx context.Context) error {
	relays := s.readRelays()
	if len(relays) == 0 {
		return errors.New("no relays to read LSP announcements from")
	}
	evs, err := s.d.Fetcher.Fetch(ctx, relays, nip01.SubscriptionFilter{Kinds: []int{niplsp.Kind}, Limit: MaxAnnouncements})
	if err != nil {
		return err
	}
	if len(evs) > MaxAnnouncements {
		evs = evs[:MaxAnnouncements]
	}
	// Trusted LSPs are read by author too, so announcement spam cannot crowd
	// out a trusted LSP's newer event (a closed status, new terms) (R2-S3).
	var trusted []string
	_ = s.d.DB.Model(&persist.LSP{}).Where("trusted = ?", true).Pluck("node_key", &trusted).Error
	if len(trusted) > 0 {
		own, err := s.d.Fetcher.Fetch(ctx, relays, nip01.SubscriptionFilter{Kinds: []int{niplsp.Kind}, Authors: trusted, Limit: 10 * len(trusted)})
		if err != nil {
			return err
		}
		evs = append(evs, own...)
	}
	if err := s.Ingest(ctx, evs); err != nil {
		return err
	}
	return s.refreshGraph(ctx)
}

// Add looks up one node's announcement and adds it to the catalogue (LP-4):
// refused when the node has none that passes the checks. It connects to
// nothing.
func (s *Service) Add(ctx context.Context, key string) (*persist.LSP, error) {
	nodeKey := NodeKeyOf(key)
	if len(nodeKey) != 64 {
		return nil, fmt.Errorf("%w: a node key is 64 hex characters, or a 66-character node id", ErrRefused)
	}
	evs, err := s.d.Fetcher.Fetch(ctx, s.readRelays(), nip01.SubscriptionFilter{Kinds: []int{niplsp.Kind}, Authors: []string{nodeKey}, Limit: 10})
	if err != nil {
		return nil, err
	}
	if err := s.Ingest(ctx, evs); err != nil {
		return nil, err
	}
	_ = s.refreshGraph(ctx)
	var row persist.LSP
	if err := s.d.DB.First(&row, "node_key = ?", nodeKey).Error; err != nil || !row.Usable {
		return nil, ErrNoAnnouncement
	}
	return &row, nil
}

// ErrRefused is a request the API refuses (400).
var ErrRefused = errors.New("refused")

// checkOptions are this node's side of the checks: its chain and network.
func (s *Service) checkOptions(ctx context.Context) (niplsp.CheckOptions, error) {
	network, err := s.d.Network(ctx)
	if err != nil {
		return niplsp.CheckOptions{}, err
	}
	return niplsp.CheckOptions{Chains: []string{s.d.Chain}, Networks: []string{network}, Now: s.d.Now().Unix()}, nil
}

// Ingest offers events to the catalogue: per node, only an announcement newer
// than the newest seen counts, and the newest seen is remembered even when it
// fails a check (no fallback to an older one).
func (s *Service) Ingest(ctx context.Context, evs []*nip01.Event) error {
	opts, err := s.checkOptions(ctx)
	if err != nil {
		return err
	}
	seen := niplsp.NewSeen(opts)
	now := s.d.Now().Unix()
	if err := s.dropFutureMarks(now); err != nil {
		return err
	}
	var rows []persist.LSP
	if err := s.d.DB.Find(&rows).Error; err != nil {
		return err
	}
	byKey := map[string]*persist.LSP{}
	for i := range rows {
		byKey[rows[i].NodeKey] = &rows[i]
		if rows[i].NewestSeenCreatedAt > 0 {
			seen.Remember(rows[i].NodeKey, niplsp.Mark{CreatedAt: rows[i].NewestSeenCreatedAt, ID: rows[i].NewestSeenID})
		}
	}
	// A pruned node's newest seen still holds: an older version is not new.
	var pruned []persist.LSPSeenMark
	if err := s.d.DB.Find(&pruned).Error; err != nil {
		return err
	}
	for _, m := range pruned {
		if byKey[m.NodeKey] == nil {
			seen.Remember(m.NodeKey, niplsp.Mark{CreatedAt: m.CreatedAt, ID: m.EventID})
		}
	}
	// Oldest first, so every node ends on its newest.
	sorted := slices.Clone(evs)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt < sorted[j].CreatedAt })
	touched := map[string]bool{}
	for _, ev := range sorted {
		if ev == nil || ev.Kind != niplsp.Kind {
			continue
		}
		if int64(ev.CreatedAt) > now+MaxFutureSecs { //nolint:gosec // event timestamps are seconds
			continue // not seen: it would hide the node's later announcements
		}
		if _, err := seen.Offer(ev); errors.Is(err, niplsp.ErrNotNewer) {
			continue
		}
		touched[ev.PubKey] = true
	}
	marks := seen.Marks()
	for key := range touched {
		mark, ok := marks[key]
		if !ok {
			continue // a step-1 failure: not the node's event
		}
		row := byKey[key]
		if row == nil {
			row = &persist.LSP{NodeKey: key}
		} else if row.NewestSeenCreatedAt == mark.CreatedAt && row.NewestSeenID == mark.ID {
			continue
		}
		row.NewestSeenCreatedAt, row.NewestSeenID = mark.CreatedAt, mark.ID
		ann, err := seen.Current(key)
		if err != nil {
			row.Usable, row.InvalidReason = false, err.Error()
		} else {
			applyAnnouncement(row, ann)
		}
		if err := s.d.DB.Save(row).Error; err != nil {
			return err
		}
	}
	// An announcement that expired since is no longer usable; it is still
	// usable at its expiration second (NIP-40, step 4).
	if err := s.d.DB.Model(&persist.LSP{}).Where("usable = ? AND expires_at > 0 AND expires_at < ?", true, now).
		Updates(map[string]any{"usable": false, "invalid_reason": "the announcement expired"}).Error; err != nil {
		return err
	}
	return s.prune()
}

// dropFutureMarks undoes newest-seen marks dated more than MaxFutureSecs
// ahead, kept before they were ignored: a row's mark falls back to its usable
// announcement, or to none when that one is future-dated too and no longer
// usable, and a pruned node's mark is dropped.
func (s *Service) dropFutureMarks(now int64) error {
	limit := now + MaxFutureSecs
	return s.d.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&persist.LSP{}).Where("announced_at > ?", limit).Updates(map[string]any{
			"usable": false, "invalid_reason": "the announcement is dated more than 15 minutes ahead",
			"announced_at": 0, "last_event_id": "", "last_event_json": "",
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&persist.LSP{}).Where("newest_seen_created_at > ?", limit).Updates(map[string]any{
			"newest_seen_created_at": gorm.Expr("announced_at"), "newest_seen_id": gorm.Expr("last_event_id"),
		}).Error; err != nil {
			return err
		}
		return tx.Where("created_at > ?", limit).Delete(&persist.LSPSeenMark{}).Error
	})
}

// MaxSeenMarks bounds the newest-seen marks kept for pruned nodes, the newest
// first, so announcement spam cannot grow them without end.
const MaxSeenMarks = 20 * MaxAnnouncements

// prune keeps at most MaxAnnouncements untrusted LSPs, the most recently
// announced, and keeps each pruned one's newest-seen mark (R2-S3) until every
// version it covers has expired: an announcement expires at most
// niplsp.MaxExpirySecs after its created_at.
func (s *Service) prune() error {
	return s.d.DB.Transaction(func(tx *gorm.DB) error {
		var keep []string
		if err := tx.Model(&persist.LSP{}).Where("trusted = ?", false).Order(clause.OrderBy{Expression: clause.Expr{
			// A future-dated row ranks last, so it cannot crowd out the others.
			SQL: "CASE WHEN announced_at > ? THEN 0 ELSE announced_at END DESC, node_key", Vars: []any{s.d.Now().Unix() + MaxFutureSecs},
			WithoutParentheses: true,
		}}).
			Limit(MaxAnnouncements).Pluck("node_key", &keep).Error; err != nil {
			return err
		}
		q := tx.Where("trusted = ?", false)
		if len(keep) > 0 {
			q = q.Where("node_key NOT IN ?", keep)
		}
		var victims []persist.LSP
		if err := q.Select("node_key", "newest_seen_created_at", "newest_seen_id").Find(&victims).Error; err != nil {
			return err
		}
		for _, v := range victims {
			if v.NewestSeenCreatedAt == 0 {
				continue
			}
			mark := persist.LSPSeenMark{NodeKey: v.NodeKey, CreatedAt: v.NewestSeenCreatedAt, EventID: v.NewestSeenID}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "node_key"}},
				DoUpdates: clause.AssignmentColumns([]string{"created_at", "event_id"}),
			}).Create(&mark).Error; err != nil {
				return err
			}
		}
		if len(victims) > 0 {
			keys := make([]string, len(victims))
			for i, v := range victims {
				keys[i] = v.NodeKey
			}
			if err := tx.Where("node_key IN ?", keys).Delete(&persist.LSP{}).Error; err != nil {
				return err
			}
		}
		// A node back in the catalogue holds its mark on its row.
		if err := tx.Where("node_key IN (?)", tx.Model(&persist.LSP{}).Select("node_key")).Delete(&persist.LSPSeenMark{}).Error; err != nil {
			return err
		}
		if err := tx.Where("created_at < ?", s.d.Now().Unix()-niplsp.MaxExpirySecs).Delete(&persist.LSPSeenMark{}).Error; err != nil {
			return err
		}
		var newest []string
		if err := tx.Model(&persist.LSPSeenMark{}).Order("created_at DESC, node_key").Limit(MaxSeenMarks).
			Pluck("node_key", &newest).Error; err != nil {
			return err
		}
		if len(newest) == MaxSeenMarks {
			return tx.Where("node_key NOT IN ?", newest).Delete(&persist.LSPSeenMark{}).Error
		}
		return nil
	})
}

func applyAnnouncement(row *persist.LSP, a *niplsp.Announcement) {
	raw, _ := json.Marshal(a.Event)
	row.Usable, row.InvalidReason = true, ""
	row.Name = a.DisplayTitle()
	row.Description = a.DisplayContent()
	row.Website = a.Display.Website
	row.Image = a.Display.Image
	row.Chain, row.Network, row.Status = a.Chain, a.Network, string(a.Status)
	if row.Status == "" {
		row.Status = string(niplsp.StatusOpen)
	}
	row.Addrs = encode(a.Addrs)
	row.Protocols = encode(a.Protocols)
	row.LSPS1 = encode(a.LSPS1)
	row.LSPS2Fees = encode(a.LSPS2)
	row.LSPS5 = encode(a.LSPS5)
	row.Languages = encode(a.Display.Languages)
	row.Relays = encode(a.Relays)
	row.OperatorPubkey = ""
	if a.Display.Operator != nil {
		row.OperatorPubkey = a.Display.Operator.Pubkey
	}
	row.LastEventID, row.LastEventJSON = a.Event.ID, string(raw)
	row.AnnouncedAt, row.ExpiresAt = a.CreatedAt, a.Expiration
	row.Host = PreferredAddr(a.Addrs)
}

// PreferredAddr is the address to connect through: an onion one first (NIP-LSP
// §Publishing), else the first.
func PreferredAddr(addrs []string) string {
	for _, a := range addrs {
		if strings.Contains(strings.ToLower(a), ".onion:") {
			return a
		}
	}
	if len(addrs) > 0 {
		return addrs[0]
	}
	return ""
}

// OrderedAddrs are the addresses, onion first.
func OrderedAddrs(addrs []string) []string {
	var onion, rest []string
	for _, a := range addrs {
		if strings.Contains(strings.ToLower(a), ".onion:") {
			onion = append(onion, a)
		} else {
			rest = append(rest, a)
		}
	}
	return append(onion, rest...)
}

// refreshGraph reads, from this node's own graph, whether each LSP's node is
// present, its channels and capacity, and resolves the node id it knows.
func (s *Service) refreshGraph(ctx context.Context) error {
	if s.d.Graph == nil {
		return nil
	}
	var rows []persist.LSP
	if err := s.d.DB.Find(&rows).Error; err != nil {
		return err
	}
	var ids []string
	for _, r := range rows {
		c := niplsp.NodeIDs(r.NodeKey)
		ids = append(ids, c[0], c[1])
	}
	if len(ids) == 0 {
		return nil
	}
	facts, err := s.d.Graph.Facts(ctx, ids)
	if err != nil {
		return err
	}
	now := s.d.Now().Unix()
	for i := range rows {
		r := &rows[i]
		r.InGraph, r.GraphChannels, r.GraphCapacity = false, 0, 0
		for _, id := range niplsp.NodeIDs(r.NodeKey) {
			if f, ok := facts[id]; ok && f.Present {
				r.InGraph, r.GraphChannels, r.GraphCapacity = true, f.Channels, f.Capacity
				r.Pubkey = id
			}
		}
		r.GraphCheckedAt = now
		if err := s.d.DB.Model(&persist.LSP{}).Where("node_key = ?", r.NodeKey).Updates(map[string]any{
			"in_graph": r.InGraph, "graph_channels": r.GraphChannels, "graph_capacity": r.GraphCapacity,
			"graph_checked_at": now, "pubkey": r.Pubkey,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// NodeKeyOf is the x-only node key of a node key or a 66-hex node id.
func NodeKeyOf(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if len(key) == 66 && (strings.HasPrefix(key, "02") || strings.HasPrefix(key, "03")) {
		return key[2:]
	}
	return key
}

func encode(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func decodeList(s string) []string {
	var l []string
	_ = json.Unmarshal([]byte(s), &l)
	return l
}

func clean(in []string) []string {
	var out []string
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}
