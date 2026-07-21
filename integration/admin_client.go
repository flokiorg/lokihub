//go:build integration

// admin_client.go is a thin client for lokihub's own admin HTTP API (the
// same one the frontend calls). It exists ONLY for test fixture setup/
// teardown - clearing circle wallet children between suite runs, so far -
// never for the actual test assertions themselves, which stay a pure
// black-box NWC client (see integration/README.md and AdminAPIConfig's doc
// comment in config.go).
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type adminClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// newAdminClient returns ok=false if admin_api isn't configured, so callers
// can no-op fixture cleanup cleanly rather than requiring it - operators who
// don't want to grant this token keep the old manual-cleanup workflow.
func newAdminClient(cfg *Config) (client *adminClient, ok bool) {
	if cfg.AdminAPI.BaseURL == "" || cfg.AdminAPI.Token == "" {
		return nil, false
	}
	return &adminClient{
		baseURL: strings.TrimRight(cfg.AdminAPI.BaseURL, "/"),
		token:   cfg.AdminAPI.Token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}, true
}

func (c *adminClient) do(method, path string, out any) error {
	return c.doBody(method, path, nil, out)
}

// doBody is do, plus a JSON-encoded request body - needed for the
// POST/PUT admin endpoints below (create app, transfer, allowlist replace),
// unlike the GET/DELETE-only calls do alone used to cover.
func (c *adminClient) doBody(method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("admin API %s %s: status %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(respBody))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("admin API %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

type adminApp struct {
	ID           uint   `json:"id"`
	WalletPubkey string `json:"walletPubkey"`
}

type adminListAppsResponse struct {
	Apps []adminApp `json:"apps"`
}

// findAppIDByWalletPubkey resolves a hub's admin-API app id from the wallet
// pubkey embedded in its own NWC connection string (nwcclient.Client's
// WalletPubkey()) - the admin API's direct-lookup-by-pubkey route
// (GET /apps/:pubkey) matches the app's own pairing pubkey, not its NWC
// wallet pubkey, so this scans the full app list instead.
func (c *adminClient) findAppIDByWalletPubkey(walletPubkey string) (uint, error) {
	var resp adminListAppsResponse
	if err := c.do(http.MethodGet, "/api/apps?limit=0", &resp); err != nil {
		return 0, err
	}
	for _, app := range resp.Apps {
		if app.WalletPubkey == walletPubkey {
			return app.ID, nil
		}
	}
	return 0, fmt.Errorf("no app found with wallet pubkey %s", walletPubkey)
}

type adminCircleChild struct {
	AppID           uint   `json:"appId"`
	RequesterPubkey string `json:"requesterPubkey"`
}

type adminListCircleChildrenResponse struct {
	Children []adminCircleChild `json:"children"`
}

// listCircleChildren returns every circle_wallet child currently minted
// under hubAppID.
func (c *adminClient) listCircleChildren(hubAppID uint) ([]adminCircleChild, error) {
	var resp adminListCircleChildrenResponse
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/apps/%d/circle/children?limit=0", hubAppID), &resp); err != nil {
		return nil, err
	}
	return resp.Children, nil
}

// deleteCircleChildPath is the admin API path for one circle_wallet child -
// shared by deleteCircleChild's retry loop and delete_test.go's one-shot
// (non-retrying) delete attempt, which needs the exact same path to observe
// an immediate "still settling" rejection instead of waiting through
// deleteCircleChild's own retries.
func deleteCircleChildPath(hubAppID, childAppID uint) string {
	return fmt.Sprintf("/api/apps/%d/circle/children/%d", hubAppID, childAppID)
}

// deleteCircleChild reclaims childAppID's remaining balance to its parent
// hub and hard-deletes it (service.ReclaimAndDeleteSubWallet), which
// cascade-deletes the CircleWalletMembership row the one-active-wallet-per-
// identity cap check queries - freeing that requester pubkey to mint a new
// circle wallet under this hub again.
//
// Retries on "a payment is still settling into this wallet" - a real,
// expected-transient guard in ReclaimAndDeleteSubWallet (service/
// jit_cleanup_service.go) against deleting a wallet out from under a
// payment that hasn't finished settling yet, whose own doc comment says
// "the caller retries". Immediately re-running this test suite right after
// a prior run's last real payment can race that settlement, so a bare,
// unretried delete call would spuriously fail cleanup here.
func (c *adminClient) deleteCircleChild(hubAppID, childAppID uint) error {
	const maxAttempts = 5
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = c.do(http.MethodDelete, deleteCircleChildPath(hubAppID, childAppID), nil)
		if err == nil || !strings.Contains(err.Error(), "still settling") {
			return err
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return err
}

type adminJITWalletClaim struct {
	ID            uint   `json:"id"`
	WalletAppID   uint   `json:"wallet_app_id"`
	IdentityValue string `json:"identity_value"`
	Claimed       bool   `json:"claimed"`
}

type adminListJITWalletClaimsResponse struct {
	Claims []adminJITWalletClaim `json:"claims"`
}

// listJITWalletClaims returns every recipient-slice claim row under
// hubAppID - jit_wallet children are deliberately excluded from the general
// /api/apps listing (see api.ListApps's own comment), so this, not
// findAppIDByWalletPubkey, is how a test resolves a jit_wallet child's admin
// app id (the claim row's WalletAppID) for deleteJITWallet.
func (c *adminClient) listJITWalletClaims(hubAppID uint) ([]adminJITWalletClaim, error) {
	var resp adminListJITWalletClaimsResponse
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/apps/%d/jit-wallets?limit=0", hubAppID), &resp); err != nil {
		return nil, err
	}
	return resp.Claims, nil
}

// deleteJITWallet reclaims walletAppID's remaining balance to hubAppID and
// hard-deletes it (api.DeleteJITWallet / service.ReclaimAndDeleteSubWallet) -
// the jit_wallet mirror of deleteCircleChild above, including the same
// "still settling" retry.
func (c *adminClient) deleteJITWallet(hubAppID, walletAppID uint) error {
	const maxAttempts = 5
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = c.do(http.MethodDelete, fmt.Sprintf("/api/apps/%d/jit-wallets/%d", hubAppID, walletAppID), nil)
		if err == nil || !strings.Contains(err.Error(), "still settling") {
			return err
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return err
}

// deleteJITWalletClaim removes one recipient's unclaimed slice from an
// otherwise-live shared jit_wallet (api.DeleteJITWalletClaim), sweeping its
// AmountMloki back to hubAppID first - the jit_wallet mirror of
// deleteCircleChild's "one child at a time" granularity, but one level
// deeper (one recipient within a wallet, not the whole wallet). If this was
// the wallet's last remaining claim, api.DeleteJITWalletClaim now also
// reclaims/deletes the wallet itself (see its own doc comment) - the fix
// for the bug where a claims-less wallet could survive forever, invisible
// to listJITWalletClaims (an inner join starting from the claims table)
// yet still blocking its hub's deletion.
func (c *adminClient) deleteJITWalletClaim(hubAppID, walletAppID, claimID uint) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/api/apps/%d/jit-wallets/%d/claims/%d", hubAppID, walletAppID, claimID), nil)
}

// deleteApp hard-deletes appID outright (api.DeleteApp) - only valid for an
// app with no children of its own (a bare jit_hub/circle_hub with none
// minted yet, or one already emptied via deleteJITWallet/deleteCircleChild
// above); apps.DeleteApp refuses otherwise. Used to tear down the ephemeral
// hubs createEphemeralJITHub mints for parent-expiry scenarios.
func (c *adminClient) deleteApp(appID uint) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/api/apps/%d", appID), nil)
}

// adminCreateAppRequest mirrors api.CreateAppRequest, kept to just the fields
// this suite's fixtures need: a throwaway, self-funded jit_hub (see
// createEphemeralJITHub) or circle_hub of either policy (see
// createEphemeralCircleHub), each with a controllable expiry.
type adminCreateAppRequest struct {
	Name          string   `json:"name"`
	MaxAmountLoki uint64   `json:"maxAmount"`
	BudgetRenewal string   `json:"budgetRenewal,omitempty"`
	ExpiresAt     string   `json:"expiresAt,omitempty"` // RFC3339, empty = never
	Scopes        []string `json:"scopes"`
	Kind          string   `json:"kind"`
	// JITPerWalletMaxMloki/JITMaxExpSecs configure the new jit_hub's own JIT
	// wallet policy - required (must be positive) whenever Kind is jit_hub,
	// see apps.CreateJITHub.
	JITPerWalletMaxMloki int `json:"jitPerWalletMaxMloki,omitempty"`
	JITMaxExpSecs        int `json:"jitMaxExpSecs,omitempty"`
	// The remaining fields configure a new circle_hub (Kind == "circle_hub")
	// and its brand-new CircleIdentity - see apps.CreateCircleHub and
	// apps.CircleIdentityRef. CircleIdentityName/CirclePolicy/ProviderPubkey
	// are only used to create a fresh identity (this suite never reuses an
	// existing one via circleIdentityId - every ephemeral hub gets its own).
	// ProviderPubkey is required iff CirclePolicy is "following"; for
	// "allowlist" it's left empty and members are granted afterward via
	// addCircleAllowlistMember.
	CircleIdentityName      string `json:"circleIdentityName,omitempty"`
	CirclePolicy            string `json:"circlePolicy,omitempty"`
	ProviderPubkey          string `json:"providerPubkey,omitempty"`
	CircleMaxExpSecs        int    `json:"circleMaxExpSecs,omitempty"`
	CircleFeesPpm           int    `json:"circleFeesPpm,omitempty"`
	CirclePerWalletMaxMloki int    `json:"circlePerWalletMaxMloki,omitempty"`
	CircleMinBudgetRenewal  string `json:"circleMinBudgetRenewal,omitempty"`
}

type adminCreateAppResponse struct {
	ID           uint   `json:"id"`
	WalletPubkey string `json:"walletPubkey"`
	PairingUri   string `json:"pairingUri"`
}

// createApp creates a new app via the admin API (POST /api/apps) with a
// server-generated pairing keypair (no Pubkey supplied), returning its app id
// and ready-to-use pairing URI.
func (c *adminClient) createApp(req adminCreateAppRequest) (adminCreateAppResponse, error) {
	var resp adminCreateAppResponse
	err := c.doBody(http.MethodPost, "/api/apps", req, &resp)
	return resp, err
}

// transfer moves amountLoki (not mloki - see api.TransferRequest) into
// toAppID's isolated balance via a real internal self-payment (api.Transfer).
// fromAppID nil roots the payment at the node's own real balance instead of
// a specific isolated app - api.Transfer's fromAppId/toAppId validation only
// runs "if appId != nil", and SendPaymentSync then pays with no app
// attributed at all - which is how every ephemeral hub in this suite
// self-funds without depending on a pre-provisioned, already-funded hub as a
// source (verified live: a fresh isolated app funded straight from nil,
// balance landed, no other app touched).
func (c *adminClient) transfer(fromAppID *uint, toAppID uint, amountLoki uint64) error {
	body := map[string]any{
		"toAppId":    toAppID,
		"amountLoki": amountLoki,
	}
	if fromAppID != nil {
		body["fromAppId"] = *fromAppID
	}
	return c.doBody(http.MethodPost, "/api/transfers", body, nil)
}

// registerIdentityAuthority registers pubkey as a trusted Identity Authority
// (POST /api/identity-authorities) - the admin-API equivalent of Settings >
// Identity Authorities, used to make a freshly-generated IA keypair trusted
// for a test's own connection_key-mode scenarios, instead of depending on
// config.local.yaml naming one already registered by hand. Pair with
// deleteIdentityAuthority in t.Cleanup.
func (c *adminClient) registerIdentityAuthority(pubkey, name string) error {
	body := map[string]any{"pubkey": pubkey, "name": name}
	return c.doBody(http.MethodPost, "/api/identity-authorities", body, nil)
}

// deleteIdentityAuthority revokes trust in pubkey - the cleanup half of
// registerIdentityAuthority.
func (c *adminClient) deleteIdentityAuthority(pubkey string) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/api/identity-authorities/%s", pubkey), nil)
}

// addCircleAllowlistMember grants pubkey membership on hubAppID's allowlist
// (GET the current list, append, PUT the full list back - there's no
// single-add endpoint, only replace-all and remove-one). Used to authorize a
// freshly-generated, disposable keypair for one test's own use, instead of
// consuming one of config.local.yaml's small fixed set of real authorized
// identities (which circle_hub_test.go's happy path already exhausts - see
// its one-active-wallet-per-identity comment). Pair with
// removeCircleAllowlistMember in t.Cleanup.
func (c *adminClient) addCircleAllowlistMember(hubAppID uint, pubkey string) error {
	var current struct {
		Pubkeys []string `json:"pubkeys"`
	}
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/apps/%d/circle/allowlist", hubAppID), &current); err != nil {
		return err
	}
	body := map[string][]string{"pubkeys": append(current.Pubkeys, pubkey)}
	return c.doBody(http.MethodPut, fmt.Sprintf("/api/apps/%d/circle/allowlist", hubAppID), body, nil)
}

// removeCircleAllowlistMember revokes a single pubkey's membership - the
// cleanup half of addCircleAllowlistMember.
func (c *adminClient) removeCircleAllowlistMember(hubAppID uint, pubkey string) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/api/apps/%d/circle/allowlist/%s", hubAppID, pubkey), nil)
}

// listAppsByNamePrefix returns every app whose name starts with prefix
// (api.ListApps's own name filter: "searching for 'Damus' will return
// 'Damus' and 'Damus (1)'" - case-insensitive LIKE prefix%). Used by
// TestZZZ_NoLeakedEphemeralFixtures to catch any ephemeralFixtureNamePrefix
// app a missing t.Cleanup left behind. Note this can't see jit_wallet
// children (api.ListApps excludes that kind unconditionally - see
// listJITWalletClaims's own doc comment) - only hubs and circle_wallet/
// isolated apps, which is everything this suite creates directly by name.
func (c *adminClient) listAppsByNamePrefix(prefix string) ([]adminApp, error) {
	filtersJSON, err := json.Marshal(map[string]string{"name": prefix})
	if err != nil {
		return nil, err
	}
	var resp adminListAppsResponse
	if err := c.do(http.MethodGet, fmt.Sprintf("/api/apps?limit=0&filters=%s", url.QueryEscape(string(filtersJSON))), &resp); err != nil {
		return nil, err
	}
	return resp.Apps, nil
}
