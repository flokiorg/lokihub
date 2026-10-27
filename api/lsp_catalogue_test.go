package api

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/discovery"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
)

const testNodeKey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

type channelsLN struct {
	*tests.MockLn
	channels []lnclient.Channel
}

func (c channelsLN) ListChannels(context.Context) ([]lnclient.Channel, error) { return c.channels, nil }

type countingLSPS struct{ checks int }

func (c *countingLSPS) Connect(context.Context, string, string) error { c.checks++; return nil }
func (c *countingLSPS) ListProtocols(context.Context, string) ([]int, error) {
	return []int{1, 2, 5}, nil
}
func (c *countingLSPS) LSPS1Info(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (c *countingLSPS) LSPS2Menu(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"opening_fee_params_menu":[]}`), nil
}

func newCatalogueAPI(t *testing.T, channels []lnclient.Channel) (*api, *manager.LSPManager, *countingLSPS) {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	lspManager := manager.NewLSPManager(svc.DB)
	live := &countingLSPS{}
	catalogue := discovery.New(discovery.Deps{DB: svc.DB, Live: live,
		Network: func(context.Context) (string, error) { return "mainnet", nil }, Chain: "bitcoin"})
	mockSvc := mocks.NewMockService(t)
	mockSvc.On("GetLSPCatalogue").Return(catalogue).Maybe()
	mockSvc.On("GetLNClient").Return(channelsLN{MockLn: svc.LNClient.(*tests.MockLn), channels: channels}).Maybe()
	mockSvc.On("GetLiquidityManager").Return(nil).Maybe()
	return &api{db: svc.DB, cfg: svc.Cfg, svc: mockSvc, lspManager: lspManager}, lspManager, live
}

func seedLSP(t *testing.T, m *manager.LSPManager, status string) {
	t.Helper()
	require.NoError(t, m.DB().Create(&persist.LSP{NodeKey: testNodeKey, Pubkey: "02" + testNodeKey, Name: "Alice",
		Status: status, Usable: true, Host: "203.0.113.7:9735"}).Error)
}

// U-26.5: trusting records the choice and runs the live check, whose result is
// stored; a closed LSP is not trusted.
func TestU26_5_TrustRunsLiveCheck(t *testing.T) {
	a, m, live := newCatalogueAPI(t, nil)
	seedLSP(t, m, "open")
	e, err := a.HandleTrustLSP(context.Background(), testNodeKey, true)
	require.NoError(t, err)
	assert.True(t, e.Trusted)
	assert.NotNil(t, e.LastCheck, "the live check result is stored and shown")
	_ = live

	b, m2, _ := newCatalogueAPI(t, nil)
	seedLSP(t, m2, "closed")
	_, err = b.HandleTrustLSP(context.Background(), testNodeKey, true)
	assert.ErrorIs(t, err, discovery.ErrRefused)
}

// U-26.8: untrusting with open channels keeps checkHasChannels's refusal.
func TestU26_8_UntrustWithChannels(t *testing.T) {
	a, m, _ := newCatalogueAPI(t, []lnclient.Channel{{RemotePubkey: "02" + testNodeKey}})
	seedLSP(t, m, "open")
	require.NoError(t, m.SetTrusted(testNodeKey, true))
	_, err := a.HandleTrustLSP(context.Background(), testNodeKey, false)
	assert.ErrorIs(t, err, discovery.ErrRefused)
	l, _ := m.GetLSP(testNodeKey)
	assert.True(t, l.Trusted)
}

// U-26.9: ListLSPs (and so info.LSPs) are the trusted LSPs only.
func TestU26_9_TrustedOnly(t *testing.T) {
	a, m, _ := newCatalogueAPI(t, nil)
	seedLSP(t, m, "open")
	l, err := a.ListLSPs()
	require.NoError(t, err)
	assert.Empty(t, l)
	require.NoError(t, m.SetTrusted(testNodeKey, true))
	l, err = a.ListLSPs()
	require.NoError(t, err)
	require.Len(t, l, 1)
	assert.Equal(t, "02"+testNodeKey, l[0].Pubkey)
}

// U-26.6 (API side): a pubkey@host input is refused; only a node key is taken.
func TestU26_6_NoPubkeyAtHost(t *testing.T) {
	a, _, _ := newCatalogueAPI(t, nil)
	_, err := a.HandleAddLSP(context.Background(), &AddLSPRequest{Pubkey: "02" + testNodeKey + "@1.2.3.4:9735"})
	assert.ErrorIs(t, err, discovery.ErrRefused)
}

// U-26.11: the removed LSP sources stay removed: no LSP env or setting, no
// services.json lsps, no pubkey@host parsing, no community flag.
func TestU26_11_RemovalGuards(t *testing.T) {
	banned := map[string]string{
		"IsCommunity":        "the community flag",
		"SyncSystemLSPs":     "services.json LSPs",
		"CommunityLSPInput":  "services.json LSPs",
		"ParseLSPURI":        "pubkey@host input",
		"saveLSPsToDatabase": "the setup LSPs array",
	}
	fset := token.NewFileSet()
	root := ".."
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "frontend" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if why, ok := banned[x.Name]; ok {
					t.Errorf("%s names %s (%s was removed, WP-26)", path, x.Name, why)
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING && strings.Contains(x.Value, `envconfig:\"LSP\"`) {
					t.Errorf("%s reads the LSP env", path)
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	cfgModels, err := os.ReadFile(filepath.Join(root, "config", "models.go"))
	require.NoError(t, err)
	assert.NotContains(t, string(cfgModels), "GetLSP() string", "the single-LSP setting")
	assert.NotContains(t, string(cfgModels), `envconfig:"LSP"`, "the LSP env")
	assert.Nil(t, withoutServicesLSPs(map[string]interface{}{"lsps": []any{1}, "rates": 1}).(map[string]interface{})["lsps"])
}
