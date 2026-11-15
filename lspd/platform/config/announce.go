package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// AnnounceConfig is a node's NIP-LSP announcement (kind 10735) settings
// (T8-2). Only display and reach live here: every LSPS value in the
// announcement comes from the code that answers LSPS, never from this block.
type AnnounceConfig struct {
	// Enabled turns announcing on for this node. Off by default.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Title is the LSP's name (`title` tag), plain text, one line, at most
	// MaxAnnounceTitleChars characters.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`

	// Content is a short description (the event content), at most
	// MaxAnnounceContentChars characters.
	Content string `json:"content,omitempty" yaml:"content,omitempty"`

	// Website (`website` tag) and Image (`image` tag, a logo) are https URLs.
	Website string `json:"website,omitempty" yaml:"website,omitempty"`
	Image   string `json:"image,omitempty" yaml:"image,omitempty"`

	// Operator is the operator's Nostr identity (`p` tag).
	Operator *AnnounceOperator `json:"operator,omitempty" yaml:"operator,omitempty"`

	// Languages are ISO-639-1 codes (`l` tags under `L` ISO-639-1).
	Languages []string `json:"languages,omitempty" yaml:"languages,omitempty"`

	// Addrs are where the node accepts Lightning connections (`addr` tags),
	// announced exactly as entered and in this order: `host:port` with an
	// IPv4 address, a bracketed IPv6 address or an onion address. Nothing
	// is derived from the node or from Host. At least one is required while
	// Enabled, since NIP-LSP requires `addr`.
	Addrs []string `json:"addrs,omitempty" yaml:"addrs,omitempty"`
}

// AnnounceOperator is the `p` tag: the operator's Nostr pubkey, then a relay
// hint.
type AnnounceOperator struct {
	// Pubkey is 64 lowercase hex characters; an npub is accepted and
	// normalized to hex by Validate.
	Pubkey string `json:"pubkey" yaml:"pubkey"`
	// Relay is a ws:// or wss:// relay where the operator can be found.
	// Required with Pubkey.
	Relay string `json:"relay,omitempty" yaml:"relay,omitempty"`
}

// Caps on the announce block. Title and content follow the caps NIP-LSP asks
// clients to apply; the rest bound the event's size.
const (
	MaxAnnounceTitleChars   = 40
	MaxAnnounceContentChars = 280
	MaxAnnounceURLLen       = 2048
	MaxAnnounceAddrs        = 16
	MaxAnnounceLanguages    = 16
)

// Validate checks every field that is set, whether or not announcing is on,
// and requires at least one addr while Enabled. It normalizes an npub
// operator pubkey to hex and trims surrounding whitespace from languages and
// URLs. Addrs are never rewritten, reordered or dropped: one that is not
// already in the exact form is refused.
func (a *AnnounceConfig) Validate() error {
	if a == nil {
		return nil
	}

	if err := checkText(a.Title, MaxAnnounceTitleChars, false); err != nil {
		return fmt.Errorf("announce.title: %w", err)
	}
	if err := checkText(a.Content, MaxAnnounceContentChars, true); err != nil {
		return fmt.Errorf("announce.content: %w", err)
	}

	a.Website = strings.TrimSpace(a.Website)
	if a.Website != "" {
		if err := checkHTTPSURL(a.Website); err != nil {
			return fmt.Errorf("announce.website: %w", err)
		}
	}
	a.Image = strings.TrimSpace(a.Image)
	if a.Image != "" {
		if err := checkHTTPSURL(a.Image); err != nil {
			return fmt.Errorf("announce.image: %w", err)
		}
	}

	if a.Operator != nil {
		if err := a.Operator.validate(); err != nil {
			return fmt.Errorf("announce.operator: %w", err)
		}
	}

	if len(a.Languages) > MaxAnnounceLanguages {
		return fmt.Errorf("announce.languages: at most %d, got %d", MaxAnnounceLanguages, len(a.Languages))
	}
	seenLang := make(map[string]bool, len(a.Languages))
	for i, l := range a.Languages {
		l = strings.TrimSpace(l)
		if !isISO6391Shape(l) {
			return fmt.Errorf("announce.languages[%d]: %q is not a two-letter lowercase ISO-639-1 code", i, l)
		}
		if seenLang[l] {
			return fmt.Errorf("announce.languages[%d]: %q listed twice", i, l)
		}
		seenLang[l] = true
		a.Languages[i] = l
	}

	if len(a.Addrs) > MaxAnnounceAddrs {
		return fmt.Errorf("announce.addrs: at most %d, got %d", MaxAnnounceAddrs, len(a.Addrs))
	}
	seenAddr := make(map[string]bool, len(a.Addrs))
	for i, addr := range a.Addrs {
		if err := CheckAnnounceAddr(addr); err != nil {
			return fmt.Errorf("announce.addrs[%d]: %w", i, err)
		}
		if seenAddr[addr] {
			return fmt.Errorf("announce.addrs[%d]: %q listed twice", i, addr)
		}
		seenAddr[addr] = true
	}
	if a.Enabled && len(a.Addrs) == 0 {
		return fmt.Errorf("announce.addrs: announcing needs at least one address (NIP-LSP requires addr); add the host:port clients should connect to")
	}

	return nil
}

func (o *AnnounceOperator) validate() error {
	o.Pubkey = strings.TrimSpace(o.Pubkey)
	if strings.HasPrefix(o.Pubkey, "npub1") {
		prefix, value, err := nip19.Decode(o.Pubkey)
		if err != nil || prefix != "npub" {
			return fmt.Errorf("pubkey: invalid npub")
		}
		o.Pubkey = value.(string)
	}
	if len(o.Pubkey) != 64 || strings.ToLower(o.Pubkey) != o.Pubkey {
		return fmt.Errorf("pubkey: want 64 lowercase hex characters or an npub")
	}
	raw, err := hex.DecodeString(o.Pubkey)
	if err != nil {
		return fmt.Errorf("pubkey: want 64 lowercase hex characters or an npub")
	}
	if _, err := schnorr.ParsePubKey(raw); err != nil {
		return fmt.Errorf("pubkey: not a valid x-only public key")
	}

	// NIP-LSP's `p` tag is the pubkey then a relay hint, so the hint is
	// required with the pubkey.
	o.Relay = strings.TrimSpace(o.Relay)
	if o.Relay == "" {
		return fmt.Errorf("relay: required with pubkey (the p tag is the pubkey then a relay hint)")
	}
	if err := CheckRelayURL(o.Relay); err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	return nil
}

// CheckAnnounceAddr checks one NIP-LSP `addr` value: `host:port`, the host an
// IPv4 address, a bracketed IPv6 address or a v3 onion address, the port
// 1..65535 written without leading zeros.
func CheckAnnounceAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q: want host:port: %v", addr, err)
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil || p == 0 || strconv.FormatUint(p, 10) != port {
		return fmt.Errorf("%q: port must be 1..65535 without leading zeros", addr)
	}

	bracketed := strings.HasPrefix(addr, "[")
	if ip := net.ParseIP(host); ip != nil {
		isV6 := strings.Contains(host, ":")
		switch {
		case isV6 && !bracketed:
			return fmt.Errorf("%q: an IPv6 address must be bracketed, e.g. [2001:db8::1]:9735", addr)
		case !isV6 && bracketed:
			return fmt.Errorf("%q: brackets hold only an IPv6 address", addr)
		case isV6 && strings.Contains(host, "%"):
			return fmt.Errorf("%q: an IPv6 zone is not allowed", addr)
		case !isV6 && ip.To4() == nil:
			return fmt.Errorf("%q: not an IPv4 address", addr)
		}
		return nil
	}
	if bracketed {
		return fmt.Errorf("%q: brackets hold only an IPv6 address", addr)
	}
	if isOnionV3(host) {
		return nil
	}
	return fmt.Errorf("%q: host must be an IPv4 address, a bracketed IPv6 address or a v3 .onion address (no DNS names)", addr)
}

// isOnionV3 reports whether host is a Tor v3 onion address: 56 lowercase
// base32 characters followed by ".onion".
func isOnionV3(host string) bool {
	label, ok := strings.CutSuffix(host, ".onion")
	if !ok || len(label) != 56 {
		return false
	}
	for _, r := range label {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			return false
		}
	}
	return true
}

func checkHTTPSURL(raw string) error {
	if len(raw) > MaxAnnounceURLLen {
		return fmt.Errorf("longer than %d characters", MaxAnnounceURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q: %v", raw, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%q: must be an https URL", raw)
	}
	if u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("%q: missing host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("%q: must not carry credentials", raw)
	}
	return nil
}

// CheckRelayURL checks a Nostr relay URL: ws:// or wss:// with a host.
func CheckRelayURL(raw string) error {
	if len(raw) > MaxAnnounceURLLen {
		return fmt.Errorf("longer than %d characters", MaxAnnounceURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q: %v", raw, err)
	}
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return fmt.Errorf("%q: must be a wss:// (or ws://) URL", raw)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%q: missing host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("%q: must not carry credentials", raw)
	}
	return nil
}

// ParseAnnounceRelays parses NOSTR_ANNOUNCE_RELAYS: a comma-separated list
// of relay URLs, the `r` tags and the relays each announcement is published
// to. Blank entries are skipped and duplicates refused; order is kept.
func ParseAnnounceRelays(env string) ([]string, error) {
	var relays []string
	seen := map[string]bool{}
	for _, part := range strings.Split(env, ",") {
		r := strings.TrimSpace(part)
		if r == "" {
			continue
		}
		if err := CheckRelayURL(r); err != nil {
			return nil, fmt.Errorf("NOSTR_ANNOUNCE_RELAYS: %w", err)
		}
		if seen[r] {
			return nil, fmt.Errorf("NOSTR_ANNOUNCE_RELAYS: %q listed twice", r)
		}
		seen[r] = true
		relays = append(relays, r)
	}
	return relays, nil
}

// checkText checks a display string: valid UTF-8, at most max characters,
// no control or zero-width characters (a newline allowed only when
// multiline).
func checkText(s string, max int, multiline bool) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("not valid UTF-8")
	}
	if n := utf8.RuneCountInString(s); n > max {
		return fmt.Errorf("at most %d characters, got %d", max, n)
	}
	for _, r := range s {
		if r == '\n' && multiline {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("contains a control or zero-width character (U+%04X)", r)
		}
	}
	return nil
}

func isISO6391Shape(s string) bool {
	return len(s) == 2 && s[0] >= 'a' && s[0] <= 'z' && s[1] >= 'a' && s[1] <= 'z'
}

// Announcing reports whether this node publishes a NIP-LSP announcement.
func (cfg *NodeConfig) Announcing() bool {
	return cfg != nil && cfg.Announce != nil && cfg.Announce.Enabled
}
