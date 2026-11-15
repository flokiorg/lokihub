package itest

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"gopkg.in/yaml.v3"
)

// Scenario is the top-level shape of a scenarios/*.yaml file. Unknown
// top-level keys are a hard error (see loadScenario) -- a typo'd field name
// should fail loudly, not silently do nothing.
type Scenario struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Steps       []VerbCall `yaml:"steps"`
	Assertions  []VerbCall `yaml:"assertions"`
}

// VerbCall is one step or assertion entry: a single-key YAML mapping of
// verb name to that verb's own params, e.g. `- connect_peer: {from: payer,
// to: lsp}`. Params is decoded into the verb's own strongly-typed struct
// by StepFn/AssertFn via decodeParamsStrict, once the verb is looked up in
// the registry -- kept as a raw *yaml.Node here since VerbCall itself has
// no way to know which struct shape to expect until Verb is resolved.
type VerbCall struct {
	Verb   string
	Params *yaml.Node
	Line   int
}

func (v *VerbCall) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return fmt.Errorf("line %d: each step/assertion must be a single-key mapping of verb name to its params (e.g. `connect_peer: {from: payer, to: lsp}`)", node.Line)
	}
	v.Verb = node.Content[0].Value
	v.Params = node.Content[1]
	v.Line = node.Line
	return nil
}

// decodeParamsStrict decodes a verb's raw params into out, rejecting
// unknown fields. yaml.Node.Decode doesn't itself honor KnownFields, so
// this re-marshals the node and decodes it through a fresh strict decoder.
func decodeParamsStrict(node *yaml.Node, out interface{}) error {
	data, err := yaml.Marshal(node)
	if err != nil {
		return fmt.Errorf("re-marshaling params: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	return nil
}

// loadScenario parses and strictly validates a single scenario file.
func loadScenario(path string) (*Scenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var s Scenario
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for i, step := range s.Steps {
		if _, ok := stepRegistry[step.Verb]; !ok {
			return nil, fmt.Errorf("%s: steps[%d] (line %d): unknown step verb %q", path, i, step.Line, step.Verb)
		}
	}
	for i, a := range s.Assertions {
		if _, ok := assertRegistry[a.Verb]; !ok {
			return nil, fmt.Errorf("%s: assertions[%d] (line %d): unknown assertion verb %q", path, i, a.Line, a.Verb)
		}
	}
	return &s, nil
}

// usesStep reports whether verb appears anywhere in the scenario's step
// list. Used by the runner to decide whether to activate optional fixtures
// (e.g. the fake mempool API) only for scenarios that actually exercise
// them, rather than changing every scenario's behavior unconditionally.
func (s *Scenario) usesStep(verb string) bool {
	for _, step := range s.Steps {
		if step.Verb == verb {
			return true
		}
	}
	return false
}

// StepFn performs one scenario step against a live ScenarioContext.
type StepFn func(t *testing.T, sc *ScenarioContext, params *yaml.Node) error

// AssertFn checks one scenario postcondition against a live
// ScenarioContext. Unlike StepFn, failures are reported via t (t.Errorf),
// not returned -- an assertion failing shouldn't necessarily abort
// evaluation of the remaining assertions in the same scenario.
type AssertFn func(t *testing.T, sc *ScenarioContext, params *yaml.Node)

var stepRegistry = map[string]StepFn{}
var assertRegistry = map[string]AssertFn{}

// ScenarioContext is the live state a running scenario's steps and
// assertions operate against: the devnet, the flspd process under test,
// cached per-role clients, and a save_as value store for passing results
// (buy offers, invoices, ...) from one step to a later one.
type ScenarioContext struct {
	T        *testing.T
	Ctx      context.Context
	Devnet   *Devnet
	Postgres *PostgresContainer
	Flspd    *FlspdProcess
	Mempool  *FakeMempoolServer

	rawClients map[string]lnrpc.LightningClient
	clients    map[string]lightning.Client
	routers    map[string]routerrpc.RouterClient

	saved map[string]interface{}
}

func newScenarioContext(t *testing.T, ctx context.Context, d *Devnet, pg *PostgresContainer, flspd *FlspdProcess, mempool *FakeMempoolServer) *ScenarioContext {
	return &ScenarioContext{
		T:          t,
		Ctx:        ctx,
		Devnet:     d,
		Postgres:   pg,
		Flspd:      flspd,
		Mempool:    mempool,
		rawClients: make(map[string]lnrpc.LightningClient),
		clients:    make(map[string]lightning.Client),
		routers:    make(map[string]routerrpc.RouterClient),
		saved:      make(map[string]interface{}),
	}
}

func (sc *ScenarioContext) raw(role string) (lnrpc.LightningClient, error) {
	if c, ok := sc.rawClients[role]; ok {
		return c, nil
	}
	c, err := sc.Devnet.RawLightningClient(role)
	if err != nil {
		return nil, fmt.Errorf("raw lightning client for role %q: %w", role, err)
	}
	sc.rawClients[role] = c
	return c, nil
}

func (sc *ScenarioContext) client(role string) (lightning.Client, error) {
	if c, ok := sc.clients[role]; ok {
		return c, nil
	}
	c, err := sc.Devnet.LightningClient(role)
	if err != nil {
		return nil, fmt.Errorf("lightning client for role %q: %w", role, err)
	}
	sc.clients[role] = c
	return c, nil
}

func (sc *ScenarioContext) router(role string) (routerrpc.RouterClient, error) {
	if c, ok := sc.routers[role]; ok {
		return c, nil
	}
	c, err := sc.Devnet.RawRouterClient(role)
	if err != nil {
		return nil, fmt.Errorf("router client for role %q: %w", role, err)
	}
	sc.routers[role] = c
	return c, nil
}

// pubkey returns role's hex-encoded identity pubkey via GetInfo.
func (sc *ScenarioContext) pubkey(role string) (string, error) {
	c, err := sc.client(role)
	if err != nil {
		return "", err
	}
	info, err := c.GetInfo()
	if err != nil {
		return "", fmt.Errorf("GetInfo for role %q: %w", role, err)
	}
	return info.Pubkey, nil
}

// channelCount returns how many channels role currently has with peer.
// Shared by the channel_count/channel_count_delta assertions and the
// count_channels step.
func (sc *ScenarioContext) channelCount(role, peer string) (int, error) {
	c, err := sc.client(role)
	if err != nil {
		return 0, err
	}
	peerPubkeyHex, err := sc.pubkey(peer)
	if err != nil {
		return 0, err
	}
	channels, err := c.ListChannels()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, ch := range channels {
		if hex.EncodeToString(ch.PeerId) == peerPubkeyHex {
			count++
		}
	}
	return count, nil
}

// save records a value under name for later steps/assertions to look up
// via get. Re-saving under an existing name is a bug in the scenario file
// (a copy-paste save_as collision), so it's a hard error rather than a
// silent overwrite.
func (sc *ScenarioContext) save(name string, value interface{}) error {
	if name == "" {
		return nil
	}
	if _, exists := sc.saved[name]; exists {
		return fmt.Errorf("save_as %q is already used earlier in this scenario", name)
	}
	sc.saved[name] = value
	return nil
}

func (sc *ScenarioContext) get(name string) (interface{}, error) {
	v, ok := sc.saved[name]
	if !ok {
		return nil, fmt.Errorf("no saved value named %q (check save_as/references match)", name)
	}
	return v, nil
}
