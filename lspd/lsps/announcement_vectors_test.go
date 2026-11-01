package lsps

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/niplsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The NIP-LSP vectors (testdata/lsp-announcement.json, from nmilat's
// testdata/vectors). They are checked through the publisher's own path:
// LSPS answers -> FromLSPSInfo -> NewAnnouncement -> ParseAnnouncement.
type lspVectors struct {
	Client vectorClient `json:"client"`
	Events []struct {
		ID    string      `json:"id"`
		Event nip01.Event `json:"event"`
		Step  int         `json:"step"`
	} `json:"events"`
	Parse []struct {
		Name   string         `json:"name"`
		Event  nip01.Event    `json:"event"`
		Client *vectorClient  `json:"client"`
		Step   int            `json:"step"`
		Want   *niplsp.Fields `json:"want"`
	} `json:"parse"`
	FromLSPSInfo []struct {
		Name   string          `json:"name"`
		LSPS1  json.RawMessage `json:"lsps1"`
		LSPS2  json.RawMessage `json:"lsps2"`
		LSPS5  json.RawMessage `json:"lsps5"`
		Fields *niplsp.Fields  `json:"fields"`
		Error  string          `json:"error"`
	} `json:"fromLSPSInfo"`
}

type vectorClient struct {
	Chains   []string `json:"chains"`
	Networks []string `json:"networks"`
	Now      int64    `json:"now"`
}

func (c vectorClient) options() niplsp.CheckOptions {
	return niplsp.CheckOptions{Chains: c.Chains, Networks: c.Networks, Now: c.Now}
}

func loadLSPVectors(t *testing.T) lspVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/lsp-announcement.json")
	require.NoError(t, err)
	var v lspVectors
	require.NoError(t, json.Unmarshal(raw, &v))
	require.Len(t, v.Events, 6, "K-LSP-1..6")
	return v
}

func stepOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ae *niplsp.AnnouncementError
	require.True(t, errors.As(err, &ae), "not a step error: %v", err)
	return ae.Step
}

// k1Fields is K-LSP-1 as the vector reads it.
func k1Fields(t *testing.T, v lspVectors) niplsp.Fields {
	t.Helper()
	for _, p := range v.Parse {
		if p.Name == "K-LSP-1" {
			require.NotNil(t, p.Want)
			return *p.Want
		}
	}
	t.Fatal("no K-LSP-1 parse case")
	return niplsp.Fields{}
}

// withVectorSig checks the built event is the vector's byte for byte (same
// id) and gives it the vector's signature, which then verifies.
func withVectorSig(t *testing.T, built *nip01.Event, vector nip01.Event) *nip01.Event {
	t.Helper()
	require.Equal(t, vector.ID, built.ID, "the built event is the vector's byte for byte")
	built.Sig = vector.Sig
	return built
}

func TestLSPVectors_ParseCases(t *testing.T) {
	v := loadLSPVectors(t)
	for _, c := range v.Parse {
		t.Run(c.Name, func(t *testing.T) {
			opts := v.Client.options()
			if c.Client != nil {
				opts = c.Client.options()
			}
			ev := c.Event
			a, err := niplsp.ParseAnnouncement(&ev, opts)
			require.Equal(t, c.Step, stepOf(t, err), "error: %v", err)
			if c.Step == 0 {
				assert.Equal(t, *c.Want, a.Fields)
			}
		})
	}
}

// K-LSP-1..6 through the publisher: the two good ones build byte for byte
// and parse back, the step-2..4 ones are refused before signing, and the
// step-5 one builds but a mainnet client sets it aside.
func TestLSPVectors_KLSPRoundTrip(t *testing.T) {
	v := loadLSPVectors(t)
	base := k1Fields(t, v)
	var lokiDialect *struct {
		LSPS1, LSPS2, LSPS5 json.RawMessage
	}
	for _, c := range v.FromLSPSInfo {
		if c.Name == "the loki dialect" {
			lokiDialect = &struct{ LSPS1, LSPS2, LSPS5 json.RawMessage }{c.LSPS1, c.LSPS2, c.LSPS5}
		}
	}
	require.NotNil(t, lokiDialect, "the loki dialect case")

	// Each K-LSP differs from K-LSP-1 in one value.
	variants := map[string]func(m *niplsp.Meta) (extraLSPS1 []niplsp.Option){
		"K-LSP-1": func(*niplsp.Meta) []niplsp.Option { return nil },
		"K-LSP-2": func(m *niplsp.Meta) []niplsp.Option { m.Status = niplsp.StatusClosed; return nil },
		"K-LSP-3": func(m *niplsp.Meta) []niplsp.Option { m.Network = ""; return nil },
		"K-LSP-4": func(*niplsp.Meta) []niplsp.Option {
			return []niplsp.Option{{Name: "max_channel_balance", Value: "20000000"}}
		},
		"K-LSP-5": func(m *niplsp.Meta) []niplsp.Option { m.Expiration++; return nil },
		"K-LSP-6": func(m *niplsp.Meta) []niplsp.Option { m.Network = "regtest"; return nil },
	}

	// Why the publisher refuses the ones a client would set aside.
	refusal := map[string]error{
		"K-LSP-3": niplsp.ErrInvalidFields,
		"K-LSP-4": niplsp.ErrDuplicateOption,
		"K-LSP-5": niplsp.ErrExpiryTooFar,
	}

	for _, k := range v.Events {
		t.Run(k.ID, func(t *testing.T) {
			vary, ok := variants[k.ID]
			require.True(t, ok)
			createdAt := int64(k.Event.CreatedAt)

			// The vector event itself fails at its step.
			ev := k.Event
			_, err := niplsp.ParseAnnouncement(&ev, v.Client.options())
			require.Equal(t, k.Step, stepOf(t, err), "vector event: %v", err)

			meta := base.Meta
			extra := vary(&meta)
			f, err := niplsp.FromLSPSInfo(base.Protocols, lokiDialect.LSPS1, lokiDialect.LSPS2, lokiDialect.LSPS5, meta)
			var built *nip01.Event
			if err == nil {
				f.LSPS1 = append(f.LSPS1, extra...)
				built, err = niplsp.NewAnnouncement(f, createdAt)
			}
			switch k.Step {
			case 2, 3, 4:
				require.ErrorIs(t, err, refusal[k.ID], "the publisher refuses what a client would ignore at step %d", k.Step)
				return
			}
			require.NoError(t, err)
			signed := withVectorSig(t, built, k.Event)
			a, err := niplsp.ParseAnnouncement(signed, v.Client.options())
			require.Equal(t, k.Step, stepOf(t, err), "built event: %v", err)
			if k.Step == 0 {
				assert.Equal(t, f, a.Fields)
			}
		})
	}
}

func TestLSPVectors_FromLSPSInfoCases(t *testing.T) {
	v := loadLSPVectors(t)
	base := k1Fields(t, v)
	k1 := v.Events[0]
	require.Equal(t, "K-LSP-1", k1.ID)
	for _, c := range v.FromLSPSInfo {
		t.Run(c.Name, func(t *testing.T) {
			f, err := niplsp.FromLSPSInfo(base.Protocols, c.LSPS1, c.LSPS2, c.LSPS5, base.Meta)
			if c.Error != "" {
				require.EqualError(t, err, c.Error)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, *c.Fields, f)
			built, err := niplsp.NewAnnouncement(f, int64(k1.Event.CreatedAt))
			require.NoError(t, err)
			_, err = niplsp.ParseAnnouncement(withVectorSig(t, built, k1.Event), v.Client.options())
			require.NoError(t, err)
		})
	}
}

func u16(v uint16) *uint16 { return &v }
func u32(v uint32) *uint32 { return &v }
func u64(v uint64) *uint64 { return &v }
func boolp(v bool) *bool   { return &v }

// flspd's own LSPS answers, configured with K-LSP-1's terms, build the NIP
// example byte for byte.
func TestLSPVectors_HandlersAnswersBuildKLSP1(t *testing.T) {
	v := loadLSPVectors(t)
	base := k1Fields(t, v)
	k1 := v.Events[0]

	cfg := &config.NodeConfig{
		JitEnabled:                           true,
		MinPaymentSizeMloki:                  20_000_000,
		Lsps1MinRequiredChannelConfirmations: u16(0),
		Lsps1MinFundingConfirmsWithinBlocks:  u16(6),
		Lsps1SupportsZeroChannelReserve:      boolp(false),
		Lsps1MaxChannelExpiryBlocks:          u32(12960),
		Lsps1MinInitialClientBalanceLoki:     u64(0),
		Lsps1MaxInitialClientBalanceLoki:     u64(1_000_000),
		Lsps1MinInitialLspBalanceLoki:        u64(0),
		Lsps1MaxInitialLspBalanceLoki:        u64(10_000_000),
		Lsps1MinChannelBalanceLoki:           u64(100_000),
		Lsps1MaxChannelBalanceLoki:           u64(10_000_000),
	}
	require.NoError(t, cfg.Validate())
	nodes := new(MockNodesService)
	nodes.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(cfg), Tokens: []string{"tok"}}})
	os := new(MockOpeningService)
	os.On("GetFeeParamsMenu", "tok", mock.Anything).Return([]*core.OpeningFeeParams{{
		MinFeeMloki: 2_000_000, Proportional: 4000, ValidUntil: "2026-10-22T00:00:00.000Z",
		MinLifetime: 4320, MaxClientToSelfDelay: 2016,
		MinPaymentSizeMloki: 10_000_000, MaxPaymentSizeMloki: 4_000_000_000, Promise: "p",
	}}, nil)
	h := &Handlers{nodesService: nodes, openingService: os, config: Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 10}}

	a, err := h.LSPSAnswers()
	require.NoError(t, err)
	require.Equal(t, base.Protocols, a.Protocols)
	f, err := niplsp.FromLSPSInfo(a.Protocols, a.LSPS1Info, a.LSPS2Info, a.LSPS5, base.Meta)
	require.NoError(t, err)
	built, err := niplsp.NewAnnouncement(f, int64(k1.Event.CreatedAt))
	require.NoError(t, err)
	parsed, err := niplsp.ParseAnnouncement(withVectorSig(t, built, k1.Event), v.Client.options())
	require.NoError(t, err)
	assert.Equal(t, base, parsed.Fields)

	// Other terms are a different event: the vector's signature does not fit.
	cfg2 := *cfg
	cfg2.Lsps1MaxChannelBalanceLoki = u64(10_000_001)
	nodes2 := new(MockNodesService)
	nodes2.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&cfg2), Tokens: []string{"tok"}}})
	h.nodesService = nodes2
	a2, err := h.LSPSAnswers()
	require.NoError(t, err)
	f2, err := niplsp.FromLSPSInfo(a2.Protocols, a2.LSPS1Info, a2.LSPS2Info, a2.LSPS5, base.Meta)
	require.NoError(t, err)
	other, err := niplsp.NewAnnouncement(f2, int64(k1.Event.CreatedAt))
	require.NoError(t, err)
	assert.NotEqual(t, k1.Event.ID, other.ID)
}
