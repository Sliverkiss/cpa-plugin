// management.go implements the WorkBuddy management API and web panel:
// account dashboard (nickname, credits, plan, check-in streak), manual/auto
// check-in (daily at 09:00 and 21:00 local time), and quota refresh.
package main

import (
	"bytes"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// billingBase hosts the Buddy-gas-station check-in and resource-package APIs.
// It is a var (not const) so tests can override it with an httptest server.
var billingBase = "https://www.codebuddy.cn"

// billingBaseGlobal is the international (www.workbuddy.ai) billing base.
var billingBaseGlobal = "https://www.workbuddy.ai"

// If the panel later wants to surface "usage export ready", re-add it and wire
// it into buildDashboardEx's response.

// -----------------------------------------------------------------------------
// Account listing via host auth callbacks
// -----------------------------------------------------------------------------

type creditsSummary struct {
	// TotalRemain is currently usable credits across all active packages.
	TotalRemain int64 `json:"total_remain"`
	// TotalUsed is consumed credits in the current cycle (sum of packages).
	TotalUsed int64 `json:"total_used"`
	// TotalSize is the credit capacity/pool (sum of package sizes). remain+used ≈ size.
	TotalSize int64 `json:"total_size"`
	// PackCount is number of resource packages included in the aggregate.
	PackCount int `json:"pack_count"`
	// FetchedAt is when this snapshot was taken (RFC3339). Upstream billing lag
	// can make remain/used look "stuck" for minutes after chat; compare this
	// timestamp — not only the numbers — when diagnosing frozen credits.
	FetchedAt string           `json:"fetched_at,omitempty"`
	Packages  []packageSummary `json:"packages"`
}

type packageSummary struct {
	Name       string `json:"name"`
	Remain     int64  `json:"remain"`
	Used       int64  `json:"used"`
	Size       int64  `json:"size"`
	CycleStart string `json:"cycle_start"`
	CycleEnd   string `json:"cycle_end"`
}

type checkinSummary struct {
	Active          bool     `json:"active"`
	TodayCheckedIn  bool     `json:"today_checked_in"`
	StreakDays      int64    `json:"streak_days"`
	DailyCredit     int64    `json:"daily_credit"`
	TodayCredit     int64    `json:"today_credit"`
	TotalCredits    int64    `json:"total_credits"`
	WeekCheckinDays int64    `json:"week_checkin_days"`
	ActivityName    string   `json:"activity_name"`
	Season          int64    `json:"season"`
	CheckinDates    []string `json:"checkin_dates,omitempty"`
}

// with a transient error (HTTP 5xx or transport error). codebuddy.cn
// intermittently returns 500s; without a retry a single hiccup surfaces as a
// panel error even though the very next request would succeed.
var billingRetryDelays = []time.Duration{300 * time.Millisecond, 900 * time.Millisecond}

// CapacityRemain/Used/Size         — lifetime package totals (Used often ≈0
//
//	for monthly-refresh free packs)
//
// CycleCapacityRemain/Used/Size    — the active billing cycle; Used is
//
//	sometimes omitted entirely
type resourcePackage struct {
	PackageName         string `json:"PackageName"`
	CapacityRemain      int64  `json:"CapacityRemain"`
	CapacityUsed        int64  `json:"CapacityUsed"`
	CapacitySize        int64  `json:"CapacitySize"`
	CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
	CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
	CycleCapacitySize   int64  `json:"CycleCapacitySize"`
	CycleStartTime      string `json:"CycleStartTime"`
	CycleEndTime        string `json:"CycleEndTime"`
}

// -----------------------------------------------------------------------------
// Auto check-in scheduler (09:00 / 21:00 local)
// -----------------------------------------------------------------------------

// Management API routes + handler
// -----------------------------------------------------------------------------

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

// managementBasePathCache holds the host-injected BasePath so handleManagement
// doesn't hardcode /v0/management. Falls back to the historical default if the
// host doesn't provide one (older CPA builds).
var (
	managementBasePathCache   = "/v0/management"
	managementBasePathCacheMu sync.RWMutex
)

func loadedManagementBasePath() string {
	managementBasePathCacheMu.RLock()
	defer managementBasePathCacheMu.RUnlock()
	return managementBasePathCache
}

func setManagementBasePath(p string) {
	p = strings.TrimRight(strings.TrimSpace(p), "/")
	if p == "" {
		return
	}
	managementBasePathCacheMu.Lock()
	managementBasePathCache = p
	managementBasePathCacheMu.Unlock()
}

func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + providerName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List WorkBuddy accounts with credits, plan and check-in status."},
			{Method: http.MethodPost, Path: base + "/refresh", Description: "Force refresh quota/cache for all accounts."},
			{Method: http.MethodPost, Path: base + "/checkin", Description: "Manually check in one account (auth_index) or all."},
			{Method: http.MethodPost, Path: base + "/checkin/config", Description: "Toggle auto check-in (enabled: true/false)."},
			{Method: http.MethodGet, Path: base + "/credits", Description: "Get real-time credits for one (auth_index query) or all accounts."},
			{Method: http.MethodPost, Path: base + "/import", Description: "Import WorkBuddy credential JSON (nested or flat) into host auth store."},
			{Method: http.MethodPost, Path: base + "/trial", Description: "Claim expert trial pack for one Global account (auth_index). One-time 250 credits / 14 days."},
			{Method: http.MethodPost, Path: base + "/select", Description: "Select the active account card used for chat routing (body: {auth_index})."},
			{Method: http.MethodPost, Path: base + "/keepalive", Description: "Manually refresh access tokens for all accounts (or one with auth_index)."},
			{Method: http.MethodGet, Path: base + "/keepalive/status", Description: "Last keepalive run summary + config."},
			{Method: http.MethodPost, Path: base + "/oauth/start", Description: "Start OAuth login flow for a region (CN or Global)."},
			{Method: http.MethodGet, Path: base + "/oauth/poll", Description: "Poll OAuth login status for a state and region."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "WorkBuddy", Description: "WorkBuddy dashboard: credits, check-in, plan, import."},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")

	// Browser UI resource routes (unauthenticated).
	resPrefix := "/v0/resource/plugins/" + providerName
	if req.Method == http.MethodGet && strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		return okEnvelope(mgmtHTMLResponse(servePanel(sub)))
	}

	// Plugin-layer auth + rate limit for mutating endpoints (v0.6.31).
	// Only enforced when management_key is configured; otherwise host middleware
	// is the sole guard (historical default).
	if req.Method == http.MethodPost || mutatingManagementPath(path) {
		ip := managementClientIP(req)
		if !allowManagementRequest(ip) {
			return okEnvelope(mgmtJSONResponse(http.StatusTooManyRequests, map[string]any{
				"error": "rate limit exceeded, try again later",
			}))
		}
		if status, msg := checkManagementAuth(req); status != 0 {
			return okEnvelope(mgmtJSONResponse(status, map[string]any{"error": msg}))
		}
	}

	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch {
	case req.Method == http.MethodGet && path == base+"/accounts":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, buildDashboardEx(false, false)))
	case req.Method == http.MethodPost && path == base+"/refresh":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, buildDashboardEx(true, true)))
	case req.Method == http.MethodPost && path == base+"/checkin":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleManualCheckin(req)))
	case req.Method == http.MethodPost && path == base+"/checkin/config":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCheckinConfig(req)))
	case req.Method == http.MethodGet && path == base+"/credits":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCreditsQuery(req)))
	case req.Method == http.MethodPost && path == base+"/import":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleImportAuth(req)))
	case req.Method == http.MethodPost && path == base+"/trial":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleClaimTrial(req)))
	case req.Method == http.MethodPost && path == base+"/select":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleSelectAuth(req)))
	case req.Method == http.MethodPost && path == base+"/keepalive":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleKeepaliveNow(req)))
	case req.Method == http.MethodGet && path == base+"/keepalive/status":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleKeepaliveStatus()))
	case req.Method == http.MethodPost && path == base+"/oauth/start":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleMgmtOAuthStart(req)))
	case req.Method == http.MethodGet && path == base+"/oauth/poll":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleMgmtOAuthPoll(req)))
	}
	return okEnvelope(mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": "not found: " + path}))
}

// -----------------------------------------------------------------------------
// Plugin-layer management auth + rate limit (v0.6.31)
// -----------------------------------------------------------------------------
//
// When management_key is configured (config_yaml or WB_MANAGEMENT_KEY env), all
// mutating endpoints under /v0/management/plugins/workbuddy/* require a matching
// Bearer token. Read-only GET endpoints (accounts/credits/panel) pass through so
// the panel can render before the user has pasted a key — the panel itself
// supplies the key on every call via Authorization header.
//
// A per-IP token-bucket rate limiter guards against brute-force when the key
// check fails repeatedly.

const (
	mgmtRateLimitCapacity = 5                // burst
	mgmtRateLimitRefill   = time.Minute / 10 // 1 token per 6s
	mgmtRateLimitTTL      = 10 * time.Minute // idle entry eviction
)

type mgmtRateEntry struct {
	tokens   float64
	lastSeen time.Time
}

var (
	mgmtRateLimit   = map[string]*mgmtRateEntry{}
	mgmtRateLimitMu sync.Mutex
)

func loadedManagementKey() string {
	managementAPIKeyMu.RLock()
	defer managementAPIKeyMu.RUnlock()
	return managementAPIKey
}

// checkManagementAuth returns an HTTP status + error message when the request
// should be rejected. status=0 means allow.
func checkManagementAuth(req pluginapi.ManagementRequest) (int, string) {
	want := loadedManagementKey()
	if want == "" {
		return 0, "" // plugin-layer auth disabled; rely on host middleware
	}
	got := strings.TrimSpace(req.Headers.Get("Authorization"))
	if !strings.HasPrefix(got, "Bearer ") {
		return http.StatusUnauthorized, "missing Bearer token"
	}
	token := strings.TrimSpace(strings.TrimPrefix(got, "Bearer "))
	if subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		return http.StatusForbidden, "invalid management key"
	}
	return 0, ""
}

// allowManagementRequest applies a per-IP token bucket. ip may be empty when the
// host doesn't forward X-Forwarded-For / RemoteAddr — in that case use a single
// global bucket.
func allowManagementRequest(ip string) bool {
	if ip == "" {
		ip = "_global"
	}
	mgmtRateLimitMu.Lock()
	defer mgmtRateLimitMu.Unlock()
	now := time.Now()
	e, ok := mgmtRateLimit[ip]
	if !ok {
		e = &mgmtRateEntry{tokens: mgmtRateLimitCapacity, lastSeen: now}
		mgmtRateLimit[ip] = e
	}
	// Refill.
	elapsed := now.Sub(e.lastSeen)
	e.tokens += float64(elapsed) / float64(mgmtRateLimitRefill)
	if e.tokens > mgmtRateLimitCapacity {
		e.tokens = mgmtRateLimitCapacity
	}
	e.lastSeen = now
	if e.tokens < 1 {
		return false
	}
	e.tokens--
	// Lazy eviction of idle entries (don't grow the map forever).
	if len(mgmtRateLimit) > 1024 {
		for k, v := range mgmtRateLimit {
			if now.Sub(v.lastSeen) > mgmtRateLimitTTL {
				delete(mgmtRateLimit, k)
			}
		}
	}
	return true
}

// managementClientIP extracts a best-effort client identifier for rate limiting.
// CPA host doesn't currently forward RemoteAddr, so fall back to X-Forwarded-For
// / X-Real-IP headers if the deployment adds them via a reverse proxy.
func managementClientIP(req pluginapi.ManagementRequest) string {
	if xff := strings.TrimSpace(req.Headers.Get("X-Forwarded-For")); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	if xr := strings.TrimSpace(req.Headers.Get("X-Real-Ip")); xr != "" {
		return xr
	}
	return ""
}

// mutatingManagementPath reports whether the path performs a write (checkin,
// import, trial claim, select, refresh, config toggle). Read endpoints pass.
func mutatingManagementPath(path string) bool {
	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch path {
	case base + "/refresh",
		base + "/checkin",
		base + "/checkin/config",
		base + "/import",
		base + "/trial",
		base + "/select",
		base + "/keepalive",
		base + "/oauth/start":
		return true
	}
	return false
}

func mgmtJSONResponse(status int, v any) pluginapi.ManagementResponse {
	body, _ := json.Marshal(v)
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	return pluginapi.ManagementResponse{StatusCode: status, Headers: h, Body: body}
}

func mgmtHTMLResponse(body []byte) pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: body}
}

// checkinLocks serializes per-account manual check-in (B4).
// Entries are pruned during dashboard prune to avoid unbounded growth
// when auth accounts are deleted/rotated.
var (
	checkinLocks sync.Map // auth_index -> *sync.Mutex
)

func handleMgmtOAuthStart(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Region string `json:"region"`
	}
	_ = json.Unmarshal(req.Body, &body)
	region := strings.ToLower(strings.TrimSpace(body.Region))
	if region != "cn" && region != "global" {
		region = "cn"
	}

	base := upstreamBaseCN
	if region == "global" {
		base = upstreamBaseGlobal
	}

	client := newLoginClient()
	stateUrl := base + "/v2/plugin/auth/state?platform=CLI"
	data, _, err := doJSON(client, http.MethodPost, stateUrl, nil, bytes.NewReader([]byte("{}")))
	if err != nil {
		return map[string]any{"success": false, "error": "auth state request failed: " + err.Error()}
	}

	var st authStateData
	_ = json.Unmarshal(data, &st)
	if st.State == "" || st.AuthURL == "" {
		return map[string]any{"success": false, "error": "missing state or authUrl from upstream"}
	}

	loginStates.Store(st.State, &loginCtx{client: client, expires: time.Now().Add(loginTTL)})
	return map[string]any{
		"success": true,
		"state":   st.State,
		"authUrl": st.AuthURL,
	}
}

func handleMgmtOAuthPoll(req pluginapi.ManagementRequest) map[string]any {
	state := ""
	if vals := req.Query["state"]; len(vals) > 0 {
		state = strings.TrimSpace(vals[0])
	}
	region := ""
	if vals := req.Query["region"]; len(vals) > 0 {
		region = strings.ToLower(strings.TrimSpace(vals[0]))
	}
	if state == "" {
		return map[string]any{"success": false, "error": "state is required"}
	}
	if region != "cn" && region != "global" {
		region = "cn"
	}

	v, ok := loginStates.Load(state)
	if !ok {
		return map[string]any{"success": false, "status": "expired", "error": "unknown state or expired"}
	}
	lc := v.(*loginCtx)
	if time.Now().After(lc.expires) {
		loginStates.Delete(state)
		return map[string]any{"success": false, "status": "expired", "error": "login expired"}
	}

	base := upstreamBaseCN
	if region == "global" {
		base = upstreamBaseGlobal
	}

	tokRaw, status, errTok := doJSON(lc.client, http.MethodGet, base+"/v2/plugin/auth/token?state="+state, nil, nil)
	if errTok != nil {
		if status == 0 || status >= 500 {
			loginStates.Delete(state)
			return map[string]any{"success": false, "status": "error", "error": errTok.Error()}
		}
		return map[string]any{"success": true, "status": "pending", "message": "waiting for login"}
	}

	var tok tokenData
	if err := json.Unmarshal(tokRaw, &tok); err != nil || tok.AccessToken == "" {
		return map[string]any{"success": true, "status": "pending", "message": "waiting for login"}
	}

	var acct accountData
	acctHeaders := func(r *http.Request) {
		commonHeaders(r)
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	if acctRaw, _, errAcct := doJSON(lc.client, http.MethodGet, base+"/v2/plugin/login/account?state="+state, acctHeaders, nil); errAcct == nil {
		_ = json.Unmarshal(acctRaw, &acct)
	}

	domain := tok.Domain
	if region == "global" && domain == "" {
		domain = "workbuddy.ai"
	}

	sa := &storedAuth{
		Auth: storedTokens{
			AccessToken:  tok.AccessToken,
			RefreshToken: tok.RefreshToken,
			ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix(),
			Domain:       domain,
		},
		Account: storedAccount{
			UID:          acct.UID,
			EnterpriseID: acct.EnterpriseID,
			Nickname:     acct.Nickname,
		},
	}
	loginStates.Delete(state)

	return map[string]any{
		"success": true,
		"status":  "success",
		"auth":    sa,
	}
}
