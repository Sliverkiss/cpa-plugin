// management.go implements the WorkBuddy management API and web panel:
// account dashboard (nickname, credits, plan, check-in streak), manual/auto
// check-in (daily at 09:00 and 21:00 local time), and quota refresh.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// billingBase hosts the Buddy-gas-station check-in and resource-package APIs.
// It is a var (not const) so tests can override it with an httptest server.
var billingBase = "https://www.codebuddy.cn"

// setBillingBase temporarily overrides billingBase for tests; returns a
// restore func.
func setBillingBase(s string) func() {
	old := billingBase
	billingBase = s
	return func() { billingBase = old }
}

// check-in schedule: 09:00 and 21:00 local time.
var checkinHours = []int{9, 21}

// plugin-level config decoded from plugin.register/reconfigure config_yaml.
var (
	checkinAuto   = true // enabled by default
	checkinAutoMu sync.RWMutex
)

// configure decodes plugin config from the lifecycle request.
func configure(raw []byte) {
	checkinAutoMu.Lock()
	defer checkinAutoMu.Unlock()
	checkinAuto = true
	schedulerModeMu.Lock()
	defer schedulerModeMu.Unlock()
	schedulerMode = schedulerModeOff // reset to default on reconfigure
	if len(raw) > 0 {
		var req struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &req); err == nil {
			for _, line := range strings.Split(string(req.ConfigYAML), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "checkin_auto:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "checkin_auto:"))
					checkinAuto = v == "true" || v == "1" || v == "yes" || v == "on"
				}
				if strings.HasPrefix(line, "scheduler_mode:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "scheduler_mode:"))
					// Strip surrounding quotes if present.
					v = strings.Trim(v, "\"'")
					if v == schedulerModeCredits {
						schedulerMode = schedulerModeCredits
					} else {
						schedulerMode = schedulerModeOff
					}
				}
			}
		}
	}
	ensureScheduler()
}

// -----------------------------------------------------------------------------
// Account listing via host auth callbacks
// -----------------------------------------------------------------------------

// wbAccount is one row of the dashboard.
type wbAccount struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name"`
	Label     string          `json:"label"`
	Nickname  string          `json:"nickname"`
	UID       string          `json:"uid"`
	Plan      string          `json:"plan"`
	Status    string          `json:"status"`
	Credits   *creditsSummary `json:"credits,omitempty"`
	Checkin   *checkinSummary `json:"checkin,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type creditsSummary struct {
	TotalRemain int64            `json:"total_remain"`
	TotalUsed   int64            `json:"total_used"`
	Packages    []packageSummary `json:"packages"`
}

type packageSummary struct {
	Name       string `json:"name"`
	Remain     int64  `json:"remain"`
	Used       int64  `json:"used"`
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

// rpcHostAuthListResponse mirrors the host's host.auth.list envelope result.
type rpcHostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type rpcHostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	JSON      json.RawMessage `json:"json"`
}

// hostAuthList returns all workbuddy credentials known to the host.
func hostAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.list: bad envelope")
	}
	var resp rpcHostAuthListResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	out := resp.Files[:0]
	for _, f := range resp.Files {
		if strings.EqualFold(f.Type, providerName) || strings.EqualFold(f.Provider, providerName) {
			out = append(out, f)
		}
	}
	return out, nil
}

// hostAuthGet fetches the credential JSON for one auth index.
func hostAuthGet(authIndex string) (*storedAuth, error) {
	body, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	raw, err := hostCall(pluginabi.MethodHostAuthGet, body)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.get: bad envelope")
	}
	var resp rpcHostAuthGetResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	return parseStored(resp.JSON)
}

// -----------------------------------------------------------------------------
// Billing / check-in API calls
// -----------------------------------------------------------------------------

func billingHeaders(req *http.Request, sa *storedAuth) {
	req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if sa.Account.UID != "" {
		req.Header.Set("X-User-Id", sa.Account.UID)
	}
	if sa.Account.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", sa.Account.EnterpriseID)
		req.Header.Set("X-Tenant-Id", sa.Account.EnterpriseID)
	}
	if sa.Auth.Domain != "" {
		req.Header.Set("X-Domain", sa.Auth.Domain)
	}
}

// billingRetryDelays backs off before retrying a billing call that failed
// with a transient error (HTTP 5xx or transport error). codebuddy.cn
// intermittently returns 500s; without a retry a single hiccup surfaces as a
// panel error even though the very next request would succeed.
var billingRetryDelays = []time.Duration{300 * time.Millisecond, 900 * time.Millisecond}

func billingCall(sa *storedAuth, path string, body any) (json.RawMessage, error) {
	data, err := billingCallOnce(sa, path, body)
	for _, d := range billingRetryDelays {
		if err == nil || !isTransientBillingErr(err) {
			break
		}
		time.Sleep(d)
		data, err = billingCallOnce(sa, path, body)
	}
	return data, err
}

// isTransientBillingErr reports whether err came from an upstream 5xx or a
// transport failure (both retryable). 4xx and business-code errors are not.
func isTransientBillingErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "http 5") || strings.HasPrefix(msg, "http=5") || strings.Contains(msg, "status 5")
}

func billingCallOnce(sa *storedAuth, path string, body any) (json.RawMessage, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequest(http.MethodPost, billingBase+path, reader)
	if err != nil {
		return nil, err
	}
	billingHeaders(req, sa)
	resp, err := sharedHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// Upstream 5xx is transient — classify it so billingCall can retry,
	// and keep the response body snippet for diagnosis.
	if resp.StatusCode >= 500 {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 120 {
			snippet = snippet[:120]
		}
		return nil, fmt.Errorf("http %d from %s: %s", resp.StatusCode, path, snippet)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("code=%d msg=%s", env.Code, env.Msg)
	}
	return env.Data, nil
}

func fetchCheckinStatus(sa *storedAuth) (*checkinSummary, error) {
	var data json.RawMessage
	var lastErr error
	for _, path := range []string{"/v2/billing/meter/checkin-activity-status", "/v2/billing/meter/checkin-status"} {
		d, err := billingCall(sa, path, nil)
		if err == nil {
			data = d
			lastErr = nil
			break
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	sum := &checkinSummary{
		Active:          jsonBool(m, "active", "Active"),
		TodayCheckedIn:  jsonBool(m, "today_checked_in", "todayCheckedIn"),
		StreakDays:      jsonI64(m, "streak_days", "streakDays"),
		DailyCredit:     jsonI64(m, "daily_credit", "dailyCredit"),
		TodayCredit:     jsonI64(m, "today_credit", "todayCredit"),
		TotalCredits:    jsonI64(m, "total_credits", "totalCredits"),
		WeekCheckinDays: jsonI64(m, "week_checkin_days", "weekCheckinDays"),
		ActivityName:    jsonStr(m, "activity_name", "activityName"),
		Season:          jsonI64(m, "season", "season"),
	}
	if dates, ok := m["checkin_dates"].([]any); ok {
		for _, d := range dates {
			if s, ok := d.(string); ok {
				sum.CheckinDates = append(sum.CheckinDates, s)
			}
		}
	} else if dates, ok := m["checkinDates"].([]any); ok {
		for _, d := range dates {
			if s, ok := d.(string); ok {
				sum.CheckinDates = append(sum.CheckinDates, s)
			}
		}
	}
	return sum, nil
}

// resourcePackage is one row of get-user-resource Accounts[].
// Upstream exposes two metric pairs per package:
//
//	CapacityRemain/Used/Size         — lifetime package totals (Used often ≈0
//	                                   for monthly-refresh free packs)
//	CycleCapacityRemain/Used/Size    — the active billing cycle; Used is
//	                                   sometimes omitted entirely
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

// packageRemainUsed picks current-cycle remain/used for one package.
// Prefer cycle metrics whenever CycleCapacitySize is present; compute used as
// size−remain so missing CycleCapacityUsed never under-reports consumption.
// Fall back to lifetime Capacity* only when cycle fields are absent entirely.
func packageRemainUsed(a resourcePackage) (remain, used int64) {
	if a.CycleCapacitySize > 0 {
		remain = a.CycleCapacityRemain
		used = a.CycleCapacitySize - a.CycleCapacityRemain
		if used < 0 {
			used = 0
		}
		// Prefer explicit CycleCapacityUsed when it is larger (defensive).
		if a.CycleCapacityUsed > used {
			used = a.CycleCapacityUsed
		}
		return remain, used
	}
	if a.CycleCapacityRemain > 0 || a.CycleCapacityUsed > 0 {
		remain = a.CycleCapacityRemain
		used = a.CycleCapacityUsed
		return remain, used
	}
	remain = a.CapacityRemain
	used = a.CapacityUsed
	if used == 0 && a.CapacitySize > remain && remain >= 0 {
		used = a.CapacitySize - remain
	}
	return remain, used
}

func fetchUserResource(sa *storedAuth) (*creditsSummary, error) {
	now := time.Now()
	// Status 0=active, 3=exhausted-but-still-listed. PageSize 100 covers the
	// multi-pack free accounts we see in production; paginate if TotalCount
	// ever exceeds it.
	const pageSize = 100
	body := map[string]any{
		"PageNumber":               1,
		"PageSize":                 pageSize,
		"ProductCode":              "p_tcaca",
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format("2006-01-02 15:04:05"),
		"PackageEndTimeRangeEnd":   now.Add(365 * 101 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	}
	data, err := billingCall(sa, "/v2/billing/meter/get-user-resource", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Response struct {
			Data struct {
				TotalCount int64             `json:"TotalCount"`
				Accounts   []resourcePackage `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	// Aggregate ALL packages (体验版 + 多个裂变包 + 其它赠送包) into one
	// account-level remain/used total. Per-package rows are kept for the panel.
	sum := &creditsSummary{}
	for _, a := range resp.Response.Data.Accounts {
		remain, used := packageRemainUsed(a)
		sum.TotalRemain += remain
		sum.TotalUsed += used
		sum.Packages = append(sum.Packages, packageSummary{
			Name:       a.PackageName,
			Remain:     remain,
			Used:       used,
			CycleStart: a.CycleStartTime,
			CycleEnd:   a.CycleEndTime,
		})
	}
	// Soft note: if TotalCount > returned rows, pagination would be needed.
	// Current free accounts have 3–4 packs; pageSize=100 is ample.
	_ = resp.Response.Data.TotalCount
	return sum, nil
}

func fetchPaymentType(sa *storedAuth) string {
	data, err := billingCall(sa, "/v2/billing/meter/get-payment-type", nil)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if s, ok := m["paymentType"].(string); ok {
		return s
	}
	return ""
}

func performCheckinCall(sa *storedAuth) (map[string]any, error) {
	data, err := billingCall(sa, "/v2/billing/meter/daily-checkin", nil)
	if err != nil {
		// billingCall returns business errors (code != 0) as Go errors; surface
		// them as a structured result so the panel can show "already checked in".
		return map[string]any{"success": false, "message": err.Error()}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func jsonBool(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case bool:
				return t
			case float64:
				return t != 0
			case string:
				return t == "true" || t == "1"
			}
		}
	}
	return false
}

func jsonI64(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return int64(t)
			case int64:
				return t
			case string:
				var n int64
				fmt.Sscanf(t, "%d", &n)
				return n
			}
		}
	}
	return 0
}

func jsonStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	return ""
}

// -----------------------------------------------------------------------------
// Dashboard assembly + caches
// -----------------------------------------------------------------------------

// accountCache caches per-account checkin/credits/plan results for 5 minutes.
// Entry doubles as last-known-good snapshot: when a refresh partially fails,
// the failed field falls back to the previous value instead of being wiped.
type accountCacheEntry struct {
	checkin *checkinSummary
	credits *creditsSummary
	plan    string
	fetched time.Time
}

var (
	accountCache    sync.Map // auth_index -> *accountCacheEntry
	accountCacheTTL = 5 * time.Minute
)

// cachedAccountDetails fetches plan/checkin/credits concurrently (upstream
// round-trip dominates; 3 serial calls ≈ 3× latency). On any individual
// failure the previous cached value is kept (stale-while-error) so a
// transient upstream 500 does not blank the panel row.
func cachedAccountDetails(authIndex string, sa *storedAuth, force bool) (plan string, ci *checkinSummary, cr *creditsSummary, errs []string) {
	var prev *accountCacheEntry
	if v, ok := accountCache.Load(authIndex); ok {
		prev = v.(*accountCacheEntry)
		if !force && time.Since(prev.fetched) < accountCacheTTL {
			return prev.plan, prev.checkin, prev.credits, nil
		}
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); plan = fetchPaymentType(sa) }()
	go func() {
		defer wg.Done()
		if c, err := fetchCheckinStatus(sa); err == nil {
			ci = c
		} else {
			errs = append(errs, "checkin: "+err.Error())
		}
	}()
	go func() {
		defer wg.Done()
		if r, err := fetchUserResource(sa); err == nil {
			cr = r
		} else {
			errs = append(errs, "credits: "+err.Error())
		}
	}()
	wg.Wait()
	// Stale-while-error: carry over previous values for fields that failed.
	if prev != nil {
		if ci == nil {
			ci = prev.checkin
		}
		if cr == nil {
			cr = prev.credits
		}
		if plan == "" {
			plan = prev.plan
		}
	}
	accountCache.Store(authIndex, &accountCacheEntry{checkin: ci, credits: cr, plan: plan, fetched: time.Now()})
	return
}

func buildDashboard(force bool) map[string]any {
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	// Prune cache entries for accounts that no longer exist (auth deleted via
	// CPA UI) or whose TTL expired long ago. Without this, accountCache grows
	// monotonically for the lifetime of the process.
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.AuthIndex] = struct{}{}
	}
	accountCache.Range(func(key, value any) bool {
		idx, _ := key.(string)
		if _, ok := live[idx]; !ok {
			accountCache.Delete(key)
			return true
		}
		if e, ok := value.(*accountCacheEntry); ok && time.Since(e.fetched) > 4*accountCacheTTL {
			accountCache.Delete(key)
		}
		return true
	})
	out := make([]wbAccount, len(files))
	// Accounts are independent — fetch their dashboards concurrently. With 4
	// accounts this cuts cold-load latency from ~4×(3 serial upstream calls)
	// to roughly one slowest account.
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func(i int, f pluginapi.HostAuthFileEntry) {
			defer wg.Done()
			acct := wbAccount{
				AuthIndex: f.AuthIndex,
				Name:      f.Name,
				Label:     f.Label,
				Status:    f.Status,
			}
			sa, err := hostAuthGet(f.AuthIndex)
			if err != nil {
				acct.Error = "load auth: " + err.Error()
				out[i] = acct
				return
			}
			acct.Nickname = sa.Account.Nickname
			acct.UID = sa.Account.UID
			plan, ci, cr, errs := cachedAccountDetails(f.AuthIndex, sa, force)
			acct.Plan = plan
			acct.Checkin = ci
			acct.Credits = cr
			acct.Error = strings.Join(errs, "; ")
			out[i] = acct
		}(i, f)
	}
	wg.Wait()
	checkinAutoMu.RLock()
	auto := checkinAuto
	checkinAutoMu.RUnlock()
	return map[string]any{
		"accounts":     out,
		"checkin_auto": auto,
		"schedule":     []string{"09:00", "21:00"},
		"server_time":  time.Now().Format("2006-01-02 15:04:05"),
	}
}

// -----------------------------------------------------------------------------
// Auto check-in scheduler (09:00 / 21:00 local)
// -----------------------------------------------------------------------------

var (
	schedulerStop chan struct{}
	schedulerMu   sync.Mutex
)

func ensureScheduler() {
	schedulerMu.Lock()
	defer schedulerMu.Unlock()
	if schedulerStop != nil {
		return // already running
	}
	schedulerStop = make(chan struct{})
	go schedulerLoop(schedulerStop)
}

// Note: there is deliberately no stopCheckinScheduler. The plugin shutdown
// export is a no-op (see cliproxyPluginShutdown) because the host invokes it
// during its own runtime teardown, where touching Go sync primitives from the
// plugin's c-shared runtime caused SIGSEGV on every restart.

func nextCheckinTime(now time.Time) time.Time {
	var earliest time.Time
	for _, h := range checkinHours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour) // slot already passed today → tomorrow
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

func schedulerLoop(stop chan struct{}) {
	for {
		next := nextCheckinTime(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			runAutoCheckin()
		}
	}
}

func runAutoCheckin() {
	checkinAutoMu.RLock()
	enabled := checkinAuto
	checkinAutoMu.RUnlock()
	if !enabled {
		return
	}
	files, err := hostAuthList()
	if err != nil {
		return
	}
	for _, f := range files {
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			continue
		}
		ci, err := fetchCheckinStatus(sa)
		if err != nil {
			continue
		}
		if ci.Active && !ci.TodayCheckedIn {
			_, _ = performCheckinCall(sa)
		}
		// Refresh cache for panel regardless.
		accountCache.Delete(f.AuthIndex)
	}
}

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

func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + providerName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List WorkBuddy accounts with credits, plan and check-in status."},
			{Method: http.MethodPost, Path: base + "/refresh", Description: "Force refresh quota/cache for all accounts."},
			{Method: http.MethodPost, Path: base + "/checkin", Description: "Manually check in one account (auth_index) or all."},
			{Method: http.MethodPost, Path: base + "/checkin/config", Description: "Toggle auto check-in (enabled: true/false)."},
			{Method: http.MethodGet, Path: base + "/credits", Description: "Get real-time credits for one (auth_index query) or all accounts."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "WorkBuddy", Description: "WorkBuddy dashboard: credits, check-in, plan."},
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

	base := "/v0/management/plugins/" + providerName
	switch {
	case req.Method == http.MethodGet && path == base+"/accounts":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, buildDashboard(false)))
	case req.Method == http.MethodPost && path == base+"/refresh":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, buildDashboard(true)))
	case req.Method == http.MethodPost && path == base+"/checkin":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleManualCheckin(req)))
	case req.Method == http.MethodPost && path == base+"/checkin/config":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCheckinConfig(req)))
	case req.Method == http.MethodGet && path == base+"/credits":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCreditsQuery(req)))
	}
	return okEnvelope(mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": "not found: " + path}))
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

func handleManualCheckin(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)

	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var targets []pluginapi.HostAuthFileEntry
	for _, f := range files {
		if authIndex == "" || f.AuthIndex == authIndex {
			targets = append(targets, f)
		}
	}
	if len(targets) == 0 {
		return map[string]any{"error": "no matching account"}
	}
	results := make([]map[string]any, 0, len(targets))
	for _, f := range targets {
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			results = append(results, map[string]any{"auth_index": f.AuthIndex, "error": err.Error()})
			continue
		}
		ci, _ := fetchCheckinStatus(sa)
		if ci != nil && ci.Active && ci.TodayCheckedIn {
			results = append(results, map[string]any{
				"auth_index": f.AuthIndex, "nickname": sa.Account.Nickname,
				"success": false, "message": "already checked in today",
			})
			accountCache.Delete(f.AuthIndex)
			continue
		}
		res, err := performCheckinCall(sa)
		out := map[string]any{"auth_index": f.AuthIndex, "nickname": sa.Account.Nickname}
		if err != nil {
			out["error"] = err.Error()
		} else {
			for k, v := range res {
				out[k] = v
			}
		}
		results = append(results, out)
		accountCache.Delete(f.AuthIndex)
	}
	return map[string]any{"results": results}
}

func handleCheckinConfig(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	_ = json.Unmarshal(req.Body, &body)
	checkinAutoMu.Lock()
	if body.Enabled != nil {
		// Runtime-only toggle: the CPA host exposes no plugin-config write
		// callback, so persisting would mean editing the host's config.yaml
		// from inside the plugin (fragile under docker volume mounts). The
		// value from config_yaml wins again on CPA restart.
		checkinAuto = *body.Enabled
	}
	cur := checkinAuto
	checkinAutoMu.Unlock()
	return map[string]any{"checkin_auto": cur, "persistent": false}
}

// handleCreditsQuery returns real-time credits for one or all accounts.
// Pass ?auth_index=<idx> to query a single account; omit for all.
func handleCreditsQuery(req pluginapi.ManagementRequest) map[string]any {
	authIndex := ""
	if vals := req.Query["auth_index"]; len(vals) > 0 {
		authIndex = strings.TrimSpace(vals[0])
	}
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	type acctCredits struct {
		AuthIndex string           `json:"auth_index"`
		Nickname  string           `json:"nickname"`
		UID       string           `json:"uid"`
		Credits   *creditsSummary  `json:"credits,omitempty"`
		Error     string           `json:"error,omitempty"`
	}
	var out []acctCredits
	for _, f := range files {
		if authIndex != "" && f.AuthIndex != authIndex {
			continue
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			out = append(out, acctCredits{AuthIndex: f.AuthIndex, Error: "load auth: " + err.Error()})
			continue
		}
		cr, err := fetchUserResource(sa)
		ac := acctCredits{AuthIndex: f.AuthIndex, Nickname: sa.Account.Nickname, UID: sa.Account.UID}
		if err != nil {
			ac.Error = err.Error()
		} else {
			ac.Credits = cr
		}
		out = append(out, ac)
	}
	return map[string]any{"accounts": out}
}

// -----------------------------------------------------------------------------
// Web panel (self-contained HTML, no external assets)
// -----------------------------------------------------------------------------

func servePanel(sub string) []byte {
	if sub != "" && sub != "/" && sub != "/panel" && sub != "/panel.html" {
		return []byte("<h1>404</h1>")
	}
	return panelHTML
}

//go:embed panel.html
var panelHTML []byte
