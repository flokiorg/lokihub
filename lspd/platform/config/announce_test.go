package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr/nip19"
	"gopkg.in/yaml.v3"
)

const (
	testOnion   = "vww6ybal4bd7szmgncyruucpgfkqahzddi37ktceo3ah7ngmcopnpyyd.onion:9735"
	testOpHex   = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	notOnCurveX = "eefdea4cdb677750a420fee807eacf21eb9898ae79b9768766e4faa04a2d4a34"
)

func validAnnounce() *AnnounceConfig {
	return &AnnounceConfig{
		Enabled:   true,
		Title:     "Alice's Liquidity",
		Content:   "Inbound channels and just-in-time channels.\nOnion preferred.",
		Website:   "https://lsp.example.com",
		Image:     "https://lsp.example.com/logo.png",
		Operator:  &AnnounceOperator{Pubkey: testOpHex, Relay: "wss://relay.example.com"},
		Languages: []string{"en", "fr"},
		Addrs:     []string{testOnion, "203.0.113.7:9735", "[2001:db8::1]:9735"},
	}
}

func TestAnnounceValidate_Valid(t *testing.T) {
	a := validAnnounce()
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	want := []string{testOnion, "203.0.113.7:9735", "[2001:db8::1]:9735"}
	for i := range want {
		if a.Addrs[i] != want[i] {
			t.Fatalf("addrs changed: %v", a.Addrs)
		}
	}
}

func TestAnnounceValidate_NilAndDisabled(t *testing.T) {
	var a *AnnounceConfig
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (&AnnounceConfig{}).Validate(); err != nil {
		t.Fatalf("empty disabled block refused: %v", err)
	}
	// Fields are still checked while announcing is off.
	if err := (&AnnounceConfig{Website: "http://x.example"}).Validate(); err == nil {
		t.Fatal("http website accepted while disabled")
	}
}

func TestAnnounceValidate_RequiresAddrWhenEnabled(t *testing.T) {
	a := validAnnounce()
	a.Addrs = nil
	err := a.Validate()
	if err == nil || !strings.Contains(err.Error(), "at least one address") {
		t.Fatalf("err = %v", err)
	}
	a.Enabled = false
	if err := a.Validate(); err != nil {
		t.Fatalf("disabled without addrs refused: %v", err)
	}
}

func TestAnnounceValidate_NpubNormalized(t *testing.T) {
	a := validAnnounce()
	npub, err := nip19.EncodePublicKey(testOpHex)
	if err != nil {
		t.Fatal(err)
	}
	a.Operator.Pubkey = npub
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Operator.Pubkey != testOpHex {
		t.Fatalf("pubkey = %s, want %s", a.Operator.Pubkey, testOpHex)
	}
}

func TestAnnounceValidate_Refusals(t *testing.T) {
	cases := map[string]func(a *AnnounceConfig){
		"title too long":         func(a *AnnounceConfig) { a.Title = strings.Repeat("a", 41) },
		"title newline":          func(a *AnnounceConfig) { a.Title = "a\nb" },
		"title zero width":       func(a *AnnounceConfig) { a.Title = "a\u200bb" },
		"content 281":            func(a *AnnounceConfig) { a.Content = strings.Repeat("é", 281) },
		"content control":        func(a *AnnounceConfig) { a.Content = "a\x07b" },
		"content bad utf8":       func(a *AnnounceConfig) { a.Content = "\xff" },
		"website http":           func(a *AnnounceConfig) { a.Website = "http://lsp.example.com" },
		"website no host":        func(a *AnnounceConfig) { a.Website = "https:///path" },
		"website relative":       func(a *AnnounceConfig) { a.Website = "lsp.example.com" },
		"website credentials":    func(a *AnnounceConfig) { a.Website = "https://u:p@lsp.example.com" },
		"image http":             func(a *AnnounceConfig) { a.Image = "http://lsp.example.com/logo.png" },
		"image data uri":         func(a *AnnounceConfig) { a.Image = "data:image/png;base64,AAAA" },
		"operator uppercase":     func(a *AnnounceConfig) { a.Operator.Pubkey = strings.ToUpper(testOpHex) },
		"operator short":         func(a *AnnounceConfig) { a.Operator.Pubkey = testOpHex[:62] },
		"operator not on curve":  func(a *AnnounceConfig) { a.Operator.Pubkey = notOnCurveX },
		"operator bad npub":      func(a *AnnounceConfig) { a.Operator.Pubkey = "npub1xyz" },
		"operator https relay":   func(a *AnnounceConfig) { a.Operator.Relay = "https://relay.example.com" },
		"operator without relay": func(a *AnnounceConfig) { a.Operator.Relay = "" },
		"operator blank relay":   func(a *AnnounceConfig) { a.Operator.Relay = "  " },
		"operator relay only":    func(a *AnnounceConfig) { a.Operator = &AnnounceOperator{Relay: "wss://relay.example.com"} },
		"language upper":         func(a *AnnounceConfig) { a.Languages = []string{"EN"} },
		"language three letters": func(a *AnnounceConfig) { a.Languages = []string{"eng"} },
		"language twice":         func(a *AnnounceConfig) { a.Languages = []string{"en", "en"} },
		"addr twice":             func(a *AnnounceConfig) { a.Addrs = []string{"203.0.113.7:9735", "203.0.113.7:9735"} },
		"too many addrs": func(a *AnnounceConfig) {
			a.Addrs = nil
			for i := 0; i <= MaxAnnounceAddrs; i++ {
				a.Addrs = append(a.Addrs, fmt.Sprintf("203.0.113.7:%d", 9000+i))
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			a := validAnnounce()
			mutate(a)
			if err := a.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestCheckAnnounceAddr(t *testing.T) {
	good := []string{
		"203.0.113.7:9735",
		"10.0.0.2:9735",
		"[2001:db8::1]:9735",
		"[::ffff:203.0.113.7]:1",
		testOnion,
		"203.0.113.7:65535",
	}
	for _, a := range good {
		if err := CheckAnnounceAddr(a); err != nil {
			t.Errorf("%q refused: %v", a, err)
		}
	}
	bad := []string{
		"",
		"203.0.113.7",
		"203.0.113.7:0",
		"203.0.113.7:65536",
		"203.0.113.7:09735",
		"203.0.113.7:+9735",
		" 203.0.113.7:9735",
		"203.0.113.7:9735 ",
		"2001:db8::1:9735",
		"[203.0.113.7]:9735",
		"[fe80::1%eth0]:9735",
		"lsp.example.com:9735",
		"localhost:9735",
		"abcdefghijklmnop.onion:9735", // v2
		strings.ToUpper(testOnion[:56]) + ".onion:9735",
		"vww6ybal4bd7szmgncyruucpgfkqahzddi37ktceo3ah7ngmcopnpyy1.onion:9735",
		"01.2.3.4:9735",
		"203.0.113.7:9735/x",
	}
	for _, a := range bad {
		if err := CheckAnnounceAddr(a); err == nil {
			t.Errorf("%q accepted", a)
		}
	}
}

func TestParseAnnounceRelays(t *testing.T) {
	got, err := ParseAnnounceRelays(" wss://a.example , ,ws://b.example:7000")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "wss://a.example" || got[1] != "ws://b.example:7000" {
		t.Fatalf("got %v", got)
	}
	if got, err := ParseAnnounceRelays(""); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, bad := range []string{"https://a.example", "wss://a.example,wss://a.example", "wss://", "a.example"} {
		if _, err := ParseAnnounceRelays(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNodeConfigValidate_ChecksAnnounce(t *testing.T) {
	cfg := &NodeConfig{Announce: &AnnounceConfig{Enabled: true}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "announce.addrs") {
		t.Fatalf("err = %v", err)
	}
	cfg = &NodeConfig{Announce: validAnnounce()}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.Announcing() {
		t.Fatal("Announcing() false")
	}
	if (&NodeConfig{}).Announcing() {
		t.Fatal("Announcing() true without a block")
	}
}

func TestAnnounceYAML(t *testing.T) {
	const doc = `
announce:
  enabled: true
  title: "Alice's Liquidity"
  content: "Inbound channels."
  website: https://lsp.example.com
  operator:
    pubkey: ` + testOpHex + `
    relay: wss://relay.example.com
  languages: [en]
  addrs:
    - ` + testOnion + `
    - 203.0.113.7:9735
`
	var cfg NodeConfig
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Announcing() || cfg.Announce.Operator.Relay != "wss://relay.example.com" ||
		len(cfg.Announce.Addrs) != 2 || cfg.Announce.Addrs[0] != testOnion {
		t.Fatalf("parsed %+v", cfg.Announce)
	}
	if err := cfg.Announce.Validate(); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "announce:") || !strings.Contains(string(out), "203.0.113.7:9735") {
		t.Fatalf("marshaled:\n%s", out)
	}
}
