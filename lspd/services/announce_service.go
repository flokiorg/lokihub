package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/niplsp"
	"github.com/rs/zerolog/log"
)

// Publisher defaults (NIP-LSP §Publishing: republish at least every 7 days,
// expiration at most 30 days after created_at; niplsp sets it to exactly 30
// days when Meta.Expiration is 0). A relay that refuses is tried again after
// DefaultAnnounceBackoffBase, doubling up to DefaultAnnounceBackoffMax.
// A closed announcement is sent until every relay accepted it, or for
// DefaultAnnounceClosedRetryFor.
const (
	DefaultAnnouncePollInterval   = 30 * time.Second
	DefaultAnnounceRepublishEvery = 7 * 24 * time.Hour
	DefaultAnnouncePublishTimeout = 15 * time.Second
	DefaultAnnounceBackoffBase    = 30 * time.Second
	DefaultAnnounceBackoffMax     = 30 * time.Minute
	DefaultAnnounceClosedRetryFor = 24 * time.Hour
)

// AnnounceNode is one node the publisher announces.
type AnnounceNode struct {
	Node    *core.Node
	Signer  ports.AnnouncementSigner
	Answers ports.LSPSAnswerSource
	// Chain and Network are the node's, from flnd GetInfo (n, network).
	Chain   string
	Network string
}

// RelayPublisher sends an event to one relay and waits for its answer.
type RelayPublisher interface {
	Publish(ctx context.Context, relayURL string, ev nostr.Event) error
}

// RelayListSource fetches a pubkey's NIP-65 relay list.
type RelayListSource interface {
	// RelayList returns the newest validly signed kind 10002 of pubkey found
	// on relays, nil when none has one, and an error when no relay answered.
	RelayList(ctx context.Context, relays []string, pubkey string) (*nostr.Event, error)
}

// maxWriteRelays caps the relays taken from a node's relay list.
const maxWriteRelays = 16

// AnnounceServiceConfig configures the publisher. Zero durations take the
// defaults above; Now defaults to time.Now.
type AnnounceServiceConfig struct {
	Relays    []string
	Store     ports.AnnounceStateStore
	Publisher RelayPublisher
	// RelayList, when set, adds the node key's NIP-65 write relays (read
	// from Relays at each signing) to the relays the event is sent to. The
	// event's r tags stay Relays.
	RelayList      RelayListSource
	PollInterval   time.Duration
	RepublishEvery time.Duration
	PublishTimeout time.Duration
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	ClosedRetryFor time.Duration
	Now            func() time.Time
}

// AnnounceService publishes each node's NIP-LSP announcement (T8-3) and
// checks every published event against the live answers (T8-4).
//
// On each run (at start, every PollInterval, and on Trigger) it works out
// the status to announce and builds the node's Fields from the live LSPS
// answers, the announce block and that status. It signs a new event when
// none is current, when an announced value differs from the current event
// (a hot-swap, a Pause/Resume/Unlist, a JIT switch), or when RepublishEvery
// has passed; then it sends the current event to every relay that has not
// accepted it and is not backing off.
//
// Closed is published once, then nothing more: after an Unlist (until a
// Resume), and when announcing is switched off on a node that published
// before (until it is switched on again; NIP-LSP: publish closed before it
// stops selling). "Once" means until every relay accepted it, or until
// ClosedRetryFor has passed with at least one acceptance: a relay that was
// down at the Unlist would otherwise keep the open announcement until it
// expires.
//
// A failed self-check pauses the announcement until Recheck, across
// restarts: the node is announced as paused (clients offer nothing new
// through it) and keeps republishing changed values, a Pause, an Unlist or
// a switch-off, instead of leaving the faulty event live. A self-check that
// cannot read the live answers is tried again on the next run.
type AnnounceService struct {
	cfg     AnnounceServiceConfig
	trigger chan struct{}
	runMu   sync.Mutex // one run at a time
	mu      sync.Mutex // guards each node's status

	nodesMu sync.RWMutex
	nodes   []*announceNodeState
}

// closeReason is why a closed announcement goes out.
type closeReason int

const (
	notClosing closeReason = iota
	closingUnlist
	closingOff
)

type relayBackoff struct {
	failures int
	retryAt  time.Time
}

type announceNodeState struct {
	AnnounceNode

	// The current signed event and what it was built from.
	event       *nostr.Event
	fields      []byte // the signed event as JSON, stored on acceptance
	fingerprint string
	signedAt    time.Time
	eventStatus core.AnnounceStatus
	reason      closeReason
	accepted    map[string]bool
	// publishTo is Relays plus the node key's NIP-65 write relays, read when
	// the current event was signed.
	publishTo []string
	results   map[string]core.AnnounceRelayResult
	recorded  bool // the first acceptance was stored (MarkPublished)
	// acceptedFields is the last event any relay accepted in this process,
	// stored or not: a switch-off can still withdraw it when MarkPublished
	// failed after the relays took it.
	acceptedFields []byte
	blocked        bool // signed while a self-check block paused the node
	// checkPending: the event was accepted and still needs its self-check.
	checkPending bool
	// closingSince is when this node's current withdrawal was first signed.
	closingSince time.Time

	backoff map[string]*relayBackoff // per relay, reset on success
	// authRequired are relays that refused with auth-required: (NIP-42).
	authRequired map[string]bool

	// lastCreatedAt is the newest created_at signed in this process; the
	// stored one covers a restart.
	lastCreatedAt int64

	status core.AnnouncePublisherStatus
}

// NewAnnounceService returns a publisher for nodes.
func NewAnnounceService(cfg AnnounceServiceConfig, nodes []AnnounceNode) *AnnounceService {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultAnnouncePollInterval
	}
	if cfg.RepublishEvery <= 0 {
		cfg.RepublishEvery = DefaultAnnounceRepublishEvery
	}
	if cfg.PublishTimeout <= 0 {
		cfg.PublishTimeout = DefaultAnnouncePublishTimeout
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = DefaultAnnounceBackoffBase
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = DefaultAnnounceBackoffMax
	}
	if cfg.ClosedRetryFor <= 0 {
		cfg.ClosedRetryFor = DefaultAnnounceClosedRetryFor
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &AnnounceService{cfg: cfg, trigger: make(chan struct{}, 1)}
	for _, n := range nodes {
		s.nodes = append(s.nodes, newNodeState(n))
	}
	return s
}

func newNodeState(n AnnounceNode) *announceNodeState {
	return &announceNodeState{AnnounceNode: n, backoff: map[string]*relayBackoff{}, authRequired: map[string]bool{}}
}

// AddNode adds a node to publish for; safe while the service runs.
func (s *AnnounceService) AddNode(n AnnounceNode) {
	s.nodesMu.Lock()
	s.nodes = append(s.nodes, newNodeState(n))
	s.nodesMu.Unlock()
	s.Trigger()
}

func (s *AnnounceService) nodeList() []*announceNodeState {
	s.nodesMu.RLock()
	defer s.nodesMu.RUnlock()
	return append([]*announceNodeState(nil), s.nodes...)
}

// Start runs the publisher until ctx ends.
func (s *AnnounceService) Start(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		s.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.trigger:
		}
	}
}

// Trigger asks for a run now (after a config update or a status change).
func (s *AnnounceService) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// RunOnce runs every node once.
func (s *AnnounceService) RunOnce(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	for _, n := range s.nodeList() {
		if err := s.runNode(ctx, n); err != nil {
			s.setError(n, err)
		}
	}
}

// Status returns the publisher status of the node, or nil when the
// publisher does not handle it.
func (s *AnnounceService) Status(nodeID []byte) *core.AnnouncePublisherStatus {
	n := s.node(nodeID)
	if n == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := n.status
	return &st
}

// Recheck lifts a self-check block on the node (stored, so it holds across
// restarts until this) and runs it again: the next publication is checked
// like any other.
func (s *AnnounceService) Recheck(ctx context.Context, nodeID []byte) error {
	n := s.node(nodeID)
	if n == nil {
		return fmt.Errorf("the publisher does not handle this node")
	}
	if err := s.cfg.Store.SetSelfCheckBlock(ctx, nodeID, ""); err != nil {
		return fmt.Errorf("lifting the self-check block: %w", err)
	}
	s.mu.Lock()
	n.status.Blocked = ""
	s.mu.Unlock()
	s.Trigger()
	return nil
}

func (s *AnnounceService) node(nodeID []byte) *announceNodeState {
	for _, n := range s.nodeList() {
		if string(n.Node.NodeId) == string(nodeID) {
			return n
		}
	}
	return nil
}

func (s *AnnounceService) runNode(ctx context.Context, n *announceNodeState) error {
	cfg := n.Node.NodeConfig.Load()
	state, err := s.cfg.Store.GetAnnounceState(ctx, n.Node.NodeId)
	if err != nil {
		return fmt.Errorf("reading the announcement status: %w", err)
	}
	s.mu.Lock()
	n.status.Blocked = state.SelfCheckBlock
	s.mu.Unlock()

	announcing := cfg.Announcing()
	if announcing && !state.OffClosedPublishedAt.IsZero() {
		// Switched on again: list the node again.
		if err := s.cfg.Store.ClearOffClosed(ctx, n.Node.NodeId); err != nil {
			return fmt.Errorf("clearing the switched-off mark: %w", err)
		}
		state.OffClosedPublishedAt = time.Time{}
	}

	status, reason := state.Status, notClosing
	blocked := state.SelfCheckBlock != ""
	switch {
	case !announcing:
		withdrawn := !state.OffClosedPublishedAt.IsZero() ||
			(state.Status == core.AnnounceClosed && !state.ClosedPublishedAt.IsZero())
		published := !state.LastPublishedAt.IsZero() || n.acceptedFields != nil
		if !published || withdrawn {
			n.event = nil // nothing (more) to withdraw
			return nil
		}
		status, reason = core.AnnounceClosed, closingOff
	case state.Status == core.AnnounceClosed:
		if !state.ClosedPublishedAt.IsZero() {
			n.event = nil
			return nil
		}
		reason = closingUnlist
	case blocked:
		// The self-check failed: announce paused, not the faulty terms.
		status = core.AnnouncePaused
	}
	var fields niplsp.Fields
	if reason != notClosing {
		fields, err = s.closedFields(cfg, n, state)
	} else {
		fields, err = s.currentFields(cfg, n, status)
	}
	if err != nil {
		return err
	}
	fp, err := fingerprint(fields)
	if err != nil {
		return err
	}

	now := s.cfg.Now()
	if n.event == nil || fp != n.fingerprint || reason != n.reason || blocked != n.blocked ||
		now.Sub(n.signedAt) >= s.cfg.RepublishEvery {
		createdAt, ahead := nextCreatedAt(now.Unix(), state.LastCreatedAt, n.lastCreatedAt)
		if ahead != 0 {
			log.Warn().Msgf("NIP-LSP: the last signed created_at %d is more than %s ahead of the clock (%d); signing at the clock instead", ahead, time.Duration(maxCreatedAtLead)*time.Second, now.Unix())
		}
		ev, err := niplsp.NewAnnouncement(fields, createdAt)
		if err != nil {
			return fmt.Errorf("building the announcement: %w", err)
		}
		if err := niplsp.Sign(ctx, ev, n.Signer.SignPayload); err != nil {
			return fmt.Errorf("signing the announcement: %w", err)
		}
		// Stored before any relay sees the event, so a restart signs after it.
		if err := s.cfg.Store.SetLastCreatedAt(ctx, n.Node.NodeId, createdAt); err != nil {
			return fmt.Errorf("recording the announcement's created_at: %w", err)
		}
		n.lastCreatedAt = createdAt
		// The event itself, not the Go encoding of Fields: its tags are the
		// NIP-LSP wire format, so a later niplsp still reads them.
		fieldsJSON, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("encoding the announced event: %w", err)
		}
		if n.event != nil && status != n.eventStatus {
			// A new status goes to every relay now, not after a backoff
			// earned by an older event.
			n.backoff = map[string]*relayBackoff{}
		}
		switch {
		case reason == notClosing:
			n.closingSince = time.Time{}
		case reason != n.reason || n.closingSince.IsZero():
			n.closingSince = now
		}
		n.publishTo = s.publishRelays(ctx, n)
		nev := toNostrEvent(ev)
		n.event, n.fields, n.fingerprint, n.signedAt = &nev, fieldsJSON, fp, now
		n.eventStatus, n.reason, n.blocked = status, reason, blocked
		n.accepted, n.results, n.recorded = map[string]bool{}, map[string]core.AnnounceRelayResult{}, false
		n.checkPending = false
	}

	s.send(ctx, n, now)

	if len(n.accepted) == 0 {
		return fmt.Errorf("no relay accepted announcement %s yet", n.event.ID)
	}
	if !n.recorded {
		n.acceptedFields = n.fields
		// recorded (and the self-check) only once the store has it: a failed
		// write is retried on the next run, so a restart still knows there
		// is something to withdraw.
		if err := s.cfg.Store.MarkPublished(ctx, n.Node.NodeId, now, n.fields); err != nil {
			return fmt.Errorf("recording the publication: %w", err)
		}
		n.recorded = true
		s.setError(n, nil)
		log.Info().Msgf("NIP-LSP: published announcement %s (status %s) to %d/%d relays", n.event.ID, n.eventStatus, len(n.accepted), len(n.publishTo))
		// A closed announcement is not shown by clients: nothing to compare.
		// One published while blocked waits for the Recheck's republish.
		n.checkPending = n.reason == notClosing && !n.blocked
	}
	if n.reason == notClosing {
		if n.checkPending {
			n.checkPending = !s.selfCheck(ctx, n, cfg, now)
		}
		return nil
	}

	// A withdrawal is done once every relay has it, or after ClosedRetryFor;
	// until then the same closed event is resent to the others.
	missing := s.missingRelays(n)
	if len(missing) > 0 {
		if now.Sub(n.closingSince) < s.cfg.ClosedRetryFor {
			return nil
		}
		log.Warn().Msgf("NIP-LSP: closed announcement %s never reached %v in %s; giving up on them", n.event.ID, missing, s.cfg.ClosedRetryFor)
	}
	switch n.reason {
	case closingUnlist:
		if _, err := s.cfg.Store.MarkClosedPublished(ctx, n.Node.NodeId, now); err != nil {
			return fmt.Errorf("recording the closed announcement: %w", err)
		}
	case closingOff:
		if err := s.cfg.Store.MarkOffClosedPublished(ctx, n.Node.NodeId, now); err != nil {
			return fmt.Errorf("recording the closed announcement: %w", err)
		}
	}
	s.setError(n, nil) // a retried write clears its error
	return nil
}

// missingRelays are the relays that have not accepted the current event,
// leaving out those that require AUTH and so never will.
func (s *AnnounceService) missingRelays(n *announceNodeState) []string {
	var missing []string
	for _, url := range n.publishTo {
		if !n.accepted[url] && !n.authRequired[url] {
			missing = append(missing, url)
		}
	}
	return missing
}

// send sends the current event to every relay that has not accepted it and
// is not backing off, and records each answer. A refusal doubles the
// relay's backoff (BackoffBase up to BackoffMax); an acceptance resets it.
func (s *AnnounceService) send(ctx context.Context, n *announceNodeState, now time.Time) {
	type outcome struct {
		relay string
		err   error
	}
	var due []string
	for _, url := range n.publishTo {
		if n.accepted[url] {
			continue
		}
		if b := n.backoff[url]; b != nil && now.Before(b.retryAt) {
			continue
		}
		due = append(due, url)
	}

	outcomes := make([]outcome, len(due))
	var wg sync.WaitGroup
	for i, url := range due {
		wg.Add(1)
		go func(i int, url string) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, s.cfg.PublishTimeout)
			defer cancel()
			outcomes[i] = outcome{url, s.cfg.Publisher.Publish(pctx, url, *n.event)}
		}(i, url)
	}
	wg.Wait()

	for _, o := range outcomes {
		if o.err == nil {
			n.accepted[o.relay] = true
			delete(n.backoff, o.relay)
			delete(n.authRequired, o.relay)
			n.results[o.relay] = core.AnnounceRelayResult{Relay: o.relay, Accepted: true}
			continue
		}
		b := n.backoff[o.relay]
		if b == nil {
			b = &relayBackoff{}
			n.backoff[o.relay] = b
		}
		b.failures++
		b.retryAt = now.Add(backoffDelay(b.failures, s.cfg.BackoffBase, s.cfg.BackoffMax))
		msg := o.err.Error()
		if strings.Contains(msg, "auth-required:") {
			// flspd answers no NIP-42 AUTH: the node key signs only kind
			// 10735 (NIP-LSP §Security Considerations). Tried rarely, and
			// not waited for by a withdrawal.
			n.authRequired[o.relay] = true
			b.retryAt = now.Add(s.cfg.BackoffMax)
			msg = "the relay requires NIP-42 AUTH, which the node key does not sign; remove it from NOSTR_ANNOUNCE_RELAYS (" + msg + ")"
		}
		n.results[o.relay] = core.AnnounceRelayResult{Relay: o.relay, Error: msg, RetryAt: b.retryAt}
		log.Warn().Err(o.err).Str("relay", o.relay).Msgf("NIP-LSP: relay did not accept the announcement; next try at %s", b.retryAt.UTC().Format(time.RFC3339))
	}

	// A fresh copy for Status readers.
	pub := &core.AnnouncePublication{
		EventID:    n.event.ID,
		CreatedAt:  n.event.CreatedAt.Time(),
		Expiration: n.event.CreatedAt.Time().Add(niplsp.MaxExpirySecs * time.Second),
		Status:     n.eventStatus,
	}
	for _, url := range n.publishTo {
		r, ok := n.results[url]
		if !ok {
			r = core.AnnounceRelayResult{Relay: url, Error: "not tried yet: backing off"}
			if b := n.backoff[url]; b != nil {
				r.RetryAt = b.retryAt
			}
		}
		pub.Relays = append(pub.Relays, r)
	}
	s.mu.Lock()
	n.status.Last = pub
	s.mu.Unlock()
}

// publishRelays is Relays plus the write relays of the node key's NIP-65
// relay list, each once. A failed fetch is logged, not fatal: Relays alone.
func (s *AnnounceService) publishRelays(ctx context.Context, n *announceNodeState) []string {
	if s.cfg.RelayList == nil {
		return s.cfg.Relays
	}
	pubkey := n.Signer.PubKey()
	fctx, cancel := context.WithTimeout(ctx, s.cfg.PublishTimeout)
	defer cancel()
	ev, err := s.cfg.RelayList.RelayList(fctx, s.cfg.Relays, pubkey)
	if err != nil {
		log.Warn().Err(err).Msg("NIP-LSP: reading the node key's relay list (kind 10002); publishing to NOSTR_ANNOUNCE_RELAYS only")
		return s.cfg.Relays
	}
	if ev == nil || ev.Kind != nostr.KindRelayListMetadata || ev.PubKey != pubkey {
		return s.cfg.Relays
	}
	return mergeRelays(s.cfg.Relays, writeRelays(ev))
}

// writeRelays are the relay list's r tags marked write or unmarked (NIP-65:
// both read and write); read-only ones are left out.
func writeRelays(ev *nostr.Event) []string {
	var out []string
	for _, t := range ev.Tags {
		if len(t) < 2 || t[0] != "r" {
			continue
		}
		if len(t) >= 3 && t[2] != "" && t[2] != "write" {
			continue
		}
		out = append(out, t[1])
	}
	return out
}

// mergeRelays is base (as given) then each extra ws(s) relay, normalised,
// that base does not already hold; at most maxWriteRelays extras.
func mergeRelays(base, extra []string) []string {
	out := append([]string(nil), base...)
	seen := map[string]bool{}
	for _, u := range base {
		seen[normalizeRelay(u)] = true
	}
	added := 0
	for _, u := range extra {
		lower := strings.ToLower(strings.TrimSpace(u))
		if !strings.HasPrefix(lower, "wss://") && !strings.HasPrefix(lower, "ws://") {
			continue
		}
		norm := normalizeRelay(u)
		if norm == "" || seen[norm] || added >= maxWriteRelays {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
		added++
	}
	return out
}

// normalizeRelay is nostr.NormalizeURL with the scheme lowercased too.
func normalizeRelay(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "://"); i > 0 {
		u = strings.ToLower(u[:i]) + u[i:]
	}
	return nostr.NormalizeURL(u)
}

// maxCreatedAtLead is how far, in seconds, a signed created_at may ever be
// ahead of the clock. It is below the 15-minute future bound clients and
// relays apply, so every event flspd signs is accepted when it is sent.
const maxCreatedAtLead = 10 * 60

// nextCreatedAt is now, or one second after the newest created_at already
// signed when that is not before now. Relays keep the newest event per node
// and, on a created_at tie, the one with the lowest id (NIP-01), so a status
// change signed in the same second as the event before it, or after the
// clock went back, must still be newer.
//
// The result is never more than maxCreatedAtLead ahead of now. A created_at
// that would need more (signed with the clock far ahead, or the clock
// stepped back further) is not followed; it is returned as ahead (the
// largest such value, 0 if none) so the caller can log it. The event is then
// signed at now: clients set the future one aside, and a relay that kept it
// prefers it until the clock passes it.
func nextCreatedAt(now int64, last ...int64) (next, ahead int64) {
	next = now
	for _, l := range last {
		if l+1-now > maxCreatedAtLead {
			ahead = max(ahead, l)
			continue
		}
		if next <= l {
			next = l + 1
		}
	}
	return next, ahead
}

// backoffDelay is base doubled per failure after the first, capped at max.
func backoffDelay(failures int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < failures && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// currentFields builds the node's Fields from its config and live answers.
func (s *AnnounceService) currentFields(cfg *config.NodeConfig, n *announceNodeState, status core.AnnounceStatus) (niplsp.Fields, error) {
	if cfg.Announce == nil {
		return niplsp.Fields{}, fmt.Errorf("the announce block is gone, so status %s cannot be announced", status)
	}
	answers, err := n.Answers.LSPSAnswers()
	if err != nil {
		return niplsp.Fields{}, fmt.Errorf("reading the LSPS answers: %w", err)
	}
	return s.buildFields(cfg, n, status, answers)
}

// closedFields are the withdrawal's Fields: the last announced values with
// status closed. They do not depend on the config or the live answers,
// which may no longer build an announcement once announcing is switched off
// (the announce block removed, addrs cleared) or the LSPS answers changed.
// A node with no stored values (published before they were kept), or whose
// stored values no longer read or build an announcement, is built from the
// current config. The stored event comes first (it survives restarts); the
// one accepted in this process is used when the store has none, which is
// when MarkPublished failed after the relays took it.
func (s *AnnounceService) closedFields(cfg *config.NodeConfig, n *announceNodeState, state *core.AnnounceState) (niplsp.Fields, error) {
	last := state.LastFields
	if len(last) == 0 {
		last = n.acceptedFields
	}
	if f, ok := storedFields(last); ok {
		f.Status = niplsp.StatusClosed
		f.Expiration = 0
		f.Relays = s.cfg.Relays
		_, err := niplsp.NewAnnouncement(f, s.cfg.Now().Unix())
		if err == nil {
			return f, nil
		}
		log.Warn().Err(err).Msg("NIP-LSP: the stored announcement values no longer build an announcement; building closed from the config")
	}
	return s.currentFields(cfg, n, core.AnnounceClosed)
}

// storedFields reads AnnounceState.LastFields: the last accepted event, read
// back from its tags, or niplsp Fields as JSON as stored before the event
// was kept. ok is false when there is nothing usable.
func storedFields(raw []byte) (niplsp.Fields, bool) {
	if len(raw) == 0 {
		return niplsp.Fields{}, false
	}
	var ev nip01.Event
	if err := json.Unmarshal(raw, &ev); err == nil && ev.Kind == niplsp.Kind && ev.Sig != "" {
		// Checked as of its own created_at: a withdrawal may come after the
		// event expired. Its own chain and network are the ones accepted.
		a, err := niplsp.ParseAnnouncement(&ev, niplsp.CheckOptions{
			Chains:   []string{eventTag(&ev, niplsp.TagChain)},
			Networks: []string{eventTag(&ev, niplsp.TagNetwork)},
			Now:      int64(ev.CreatedAt), //nolint:gosec // event timestamps are seconds
		})
		if err != nil {
			log.Warn().Err(err).Msg("NIP-LSP: the stored announcement does not read back")
			return niplsp.Fields{}, false
		}
		return a.Fields, true
	}
	var f niplsp.Fields
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Warn().Err(err).Msg("NIP-LSP: the stored announcement values do not decode")
		return niplsp.Fields{}, false
	}
	return f, true
}

// eventTag is the first value of the tag name, or "".
func eventTag(ev *nip01.Event, name string) string {
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

// buildFields maps the live answers, the announce block and the status to
// niplsp Fields.
func (s *AnnounceService) buildFields(cfg *config.NodeConfig, n *announceNodeState, status core.AnnounceStatus, a *ports.LSPSAnswers) (niplsp.Fields, error) {
	ann := cfg.Announce
	display := niplsp.Display{
		Title:     ann.Title,
		Content:   ann.Content,
		Website:   ann.Website,
		Image:     ann.Image,
		Languages: ann.Languages,
	}
	if ann.Operator != nil && ann.Operator.Pubkey != "" {
		display.Operator = &niplsp.Operator{Pubkey: ann.Operator.Pubkey, Relay: ann.Operator.Relay}
	}
	fields, err := niplsp.FromLSPSInfo(a.Protocols, a.LSPS1Info, a.LSPS2Info, a.LSPS5, niplsp.Meta{
		NodeKey: hex.EncodeToString(n.Node.NodeId),
		Addrs:   ann.Addrs,
		Chain:   n.Chain,
		Network: n.Network,
		Status:  niplsp.Status(status),
		Display: display,
		Relays:  s.cfg.Relays,
	})
	if err != nil {
		return niplsp.Fields{}, fmt.Errorf("building the announcement fields: %w", err)
	}
	return fields, nil
}

// selfCheck (T8-4) parses the published event as a client would and
// compares it with the live answers read again now. A difference whose
// cause is a value that changed since the build is not a fault: it asks for
// a republish. Any other failure blocks the node, which is then announced
// as paused until Recheck. It reports whether the check is done: false when
// the live answers could not be read, to try again on the next run.
func (s *AnnounceService) selfCheck(ctx context.Context, n *announceNodeState, cfg *config.NodeConfig, now time.Time) bool {
	ev, publishedFP := fromNostrEvent(*n.event), n.fingerprint
	check := &core.AnnounceSelfCheck{CheckedAt: now}
	block := func(problems ...string) {
		check.Problems = problems
		reason := "self-check failed: " + problems[0]
		s.mu.Lock()
		n.status.SelfCheck = check
		n.status.Blocked = reason
		s.mu.Unlock()
		// Stored, so a restart does not lift it; only Recheck does.
		if err := s.cfg.Store.SetSelfCheckBlock(ctx, n.Node.NodeId, reason); err != nil {
			log.Error().Err(err).Msg("NIP-LSP: storing the self-check block")
		}
		log.Error().Msgf("NIP-LSP: announcement %s failed its self-check, announcing paused until rechecked: %v", ev.ID, problems)
		s.Trigger() // publish paused now
	}

	a, err := niplsp.ParseAnnouncement(ev, niplsp.CheckOptions{
		Chains:   []string{n.Chain},
		Networks: []string{n.Network},
		Now:      now.Unix(),
	})
	if err != nil {
		block(fmt.Sprintf("the published event does not pass a client's checks: %v", err))
		return true
	}

	fresh, err := n.Answers.LSPSAnswers()
	if err != nil {
		// Not a fault of the announcement: check again on the next run.
		check.Problems = []string{fmt.Sprintf("could not read the live answers, checking again: %v", err)}
		s.mu.Lock()
		n.status.SelfCheck = check
		s.mu.Unlock()
		return false
	}
	live := niplsp.Live{Protocols: fresh.Protocols}
	for _, p := range fresh.Protocols {
		switch p {
		case niplsp.ProtocolLSPS1:
			live.LSPS1, err = niplsp.ParseLSPS1Info(fresh.LSPS1Info)
		case niplsp.ProtocolLSPS2:
			live.LSPS2Menu, err = niplsp.ParseLSPS2Menu(fresh.LSPS2Info)
		}
		if err != nil {
			block(fmt.Sprintf("reading the live LSPS%d answer: %v", p, err))
			return true
		}
	}

	diff := niplsp.CheckLive(a, live)
	if diff.Same() {
		check.OK = true
		s.mu.Lock()
		n.status.SelfCheck = check
		s.mu.Unlock()
		return true
	}

	var problems []string
	for _, p := range diff.MissingProtocol {
		problems = append(problems, fmt.Sprintf("protocol %d is announced but not in list_protocols", p))
	}
	for _, f := range diff.Changed() {
		problems = append(problems, fmt.Sprintf("%s: announced %s, live %s (%s for the buyer)", f.Field, f.Announced, f.Live, f.Verdict))
	}

	// A live value that changed between the build and this check is a new
	// announced value, not a fault: republish it.
	if freshFields, err := s.buildFields(cfg, n, core.AnnounceStatus(a.Status), fresh); err == nil {
		if freshFP, err := fingerprint(freshFields); err == nil && freshFP != publishedFP {
			check.Problems = append(problems, "the live answers changed after publishing; republishing")
			s.mu.Lock()
			n.status.SelfCheck = check
			s.mu.Unlock()
			s.Trigger()
			return true
		}
	}
	block(problems...)
	return true
}

func (s *AnnounceService) setError(n *announceNodeState, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != "" && msg != n.status.LastError {
		log.Error().Err(err).Msgf("NIP-LSP: announcing node %x", n.Node.NodeId)
	}
	n.status.LastError = msg
}

// fingerprint identifies the announced values; the expiration is left out
// (it is 0 in Meta until NewAnnouncement sets it), so only a real change of
// an announced value changes it.
func fingerprint(f niplsp.Fields) (string, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func fromNostrEvent(ev nostr.Event) *nip01.Event {
	tags := make([][]string, len(ev.Tags))
	for i, t := range ev.Tags {
		tags[i] = append([]string(nil), t...)
	}
	return &nip01.Event{
		ID:        ev.ID,
		PubKey:    ev.PubKey,
		CreatedAt: uint64(ev.CreatedAt),
		Kind:      ev.Kind,
		Tags:      tags,
		Content:   ev.Content,
		Sig:       ev.Sig,
	}
}

func toNostrEvent(ev *nip01.Event) nostr.Event {
	tags := make(nostr.Tags, len(ev.Tags))
	for i, t := range ev.Tags {
		tags[i] = nostr.Tag(append([]string(nil), t...))
	}
	return nostr.Event{
		ID:        ev.ID,
		PubKey:    ev.PubKey,
		CreatedAt: nostr.Timestamp(ev.CreatedAt),
		Kind:      ev.Kind,
		Tags:      tags,
		Content:   ev.Content,
		Sig:       ev.Sig,
	}
}
