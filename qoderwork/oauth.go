// oauth.go implements QoderWork's auth flows. Two entry points, one outcome:
//
//  1. PAT import (management panel): user pastes a PAT (`pt-...`) created on
//     qoder.com.cn; the plugin exchanges it for a jobToken pair (jt-/jrt-)
//     and stores both.
//
//  2. OAuth-like flow (AuthProvider.StartLogin): QoderWork has no real
//     OAuth authorization-code flow — its web login is Aliyun SSO SMS →
//     web session cookie → manual PAT creation. So handleStartLogin returns
//     the PAT-creation page URL; the user creates a PAT in their browser
//     and pastes it back. handlePollLogin accepts the pasted PAT and runs
//     the same exchange as path 1.
//
// Refresh uses jrt- (48h TTL) to get a fresh jt- (24h TTL). When jrt-
// expires, the underlying PAT is still valid — we fall back to a fresh
// exchange using the stored PAT.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// jobTokenResponse mirrors the upstream /api/v1/jobToken/{exchange,refresh}
// response payload.
type jobTokenResponse struct {
	Token                 string `json:"token"`         // jt-..., 24h
	RefreshToken          string `json:"refresh_token"` // jrt-..., 48h
	ExpiresAt             string `json:"expires_at"`    // RFC3339
	ExpiresIn             int64  `json:"expires_in"`    // ms
	RefreshTokenExpiresAt string `json:"refresh_token_expires_at"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"` // ms
}

// doRawJSON sends method to fullURL with the given headers and returns the
// raw response body. Used for endpoints that return plain JSON (no envelope)
// like /api/v1/jobToken/exchange and /api/v1/jobToken/refresh.
func doRawJSON(client *http.Client, method, fullURL string, headers func(*http.Request), body io.Reader) (json.RawMessage, int, error) {
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	if headers != nil {
		headers(req)
	} else {
		commonHeaders(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream %d body=%s", resp.StatusCode, truncateRedacted(string(raw), 200))
	}
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream redirect %d (location: %s)", resp.StatusCode, resp.Header.Get("Location"))
	}
	return raw, resp.StatusCode, nil
}

// exchangePATForJobToken calls POST /api/v1/jobToken/exchange with a PAT and
// returns the resulting jt-/jrt- pair.
func exchangePATForJobToken(pat string) (*jobTokenResponse, error) {
	body, _ := json.Marshal(map[string]string{"personal_token": pat})
	data, _, err := doRawJSON(sharedHTTPClient(), http.MethodPost, endpointJobTokenExchange, nil, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out jobTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("jobToken exchange parse: %w", err)
	}
	if out.Token == "" || out.RefreshToken == "" {
		return nil, fmt.Errorf("jobToken exchange: empty token pair in response")
	}
	return &out, nil
}

// refreshJobToken calls POST /api/v1/jobToken/refresh with a jrt-.
func refreshJobToken(jrt string) (*jobTokenResponse, error) {
	body, _ := json.Marshal(map[string]string{"refresh_token": jrt})
	data, _, err := doRawJSON(sharedHTTPClient(), http.MethodPost, endpointJobTokenRefresh, nil, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out jobTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("jobToken refresh parse: %w", err)
	}
	if out.Token == "" {
		return nil, fmt.Errorf("jobToken refresh: empty token in response")
	}
	return &out, nil
}

// userInfoResponse is the minimal subset of GET /api/v1/userinfo we need to
// populate auth identity (uid, nickname).
// Upstream returns: {"id":"...","name":"aliyun...","username":"...",
// "organization_id":"","organization_name":"",...}
type userInfoResponse struct {
	ID               string `json:"id"`       // uuid — used as our UID
	Name             string `json:"name"`     // display name
	Username         string `json:"username"` // alternate uuid
	Avatar           string `json:"avatar"`
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	UserType         string `json:"user_type"` // may be absent; falls back to "personal_professional_trial"
}

// fetchUserInfo queries /api/v1/userinfo with a jt- Bearer to populate the
// auth's identity fields (uid, nickname, user_type for COSY signing).
// The endpoint returns plain JSON (no envelope) — same as jobToken/exchange.
func fetchUserInfo(jt string) (*userInfoResponse, error) {
	req, err := http.NewRequest(http.MethodGet, endpointUserInfo, nil)
	if err != nil {
		return nil, err
	}
	commonHeaders(req)
	req.Header.Set("Authorization", "Bearer "+jt)
	resp, err := sharedHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("userinfo: http %d body=%s", resp.StatusCode, truncateRedacted(string(raw), 200))
	}
	var out userInfoResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("userinfo parse: %w (body=%s)", err, truncateRedacted(string(raw), 200))
	}
	return &out, nil
}

// buildStoredAuthFromJobToken constructs a storedAuth from a PAT + jobToken
// pair + user identity. Called by both PAT import and OAuth-like poll.
func buildStoredAuthFromJobToken(pat string, tok *jobTokenResponse, ui *userInfoResponse) *storedAuth {
	expiresAt := time.Now().Add(24 * time.Hour).Unix()
	if tok.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Millisecond).Unix()
	}

	nickname := ""
	uid := ""
	if ui != nil {
		uid = ui.ID
		nickname = ui.Name
	}

	// PersonalToken is stored inside RefreshToken only when refresh is via PAT
	// (no jrt available). For QoderWork we have a real jrt- (refresh token),
	// so RefreshToken carries the jrt- and the PAT itself is kept in Domain
	// alongside the realm for later re-exchange when jrt- expires (48h).
	return &storedAuth{
		Auth: storedTokens{
			AccessToken:   tok.Token,
			RefreshToken:  tok.RefreshToken,
			PersonalToken: pat, // PAT stored for automatic re-exchange when jrt- expires (48h)
			ExpiresAt:     expiresAt,
			Domain:        "qoder.com.cn",
		},
		Account: storedAccount{
			UID:      uid,
			Nickname: nickname,
		},
	}
}

func uiUserType(ui *userInfoResponse) string {
	if ui == nil || ui.UserType == "" {
		return "personal_professional_trial"
	}
	return ui.UserType
}

// -----------------------------------------------------------------------------
// Device-authorization login (real OAuth — no PAT required)
//
// Reverse-engineered from the official QoderWork CN desktop client
// (/tmp/qw_extract .../main.js) and proven live against the Global realm by
// /root/qoder-register/qoder_device_oauth.py (2026-07-22, issued dt-/drt-
// tokens). CN realm constants from the same main.js:
//
//	WEBSITE_DOMAIN  = qoder.com.cn        (auth pages)
//	OPENAPI_DOMAIN  = openapi.qoder.com.cn (token endpoints)
//	CLIENT_ID prod  = 1c5e33e1-364d-4ce6-b02c-acaa81274a5c (shared with Global)
//	REDIRECT_URI    = qoder-work-cn://
//
// Flow: StartLogin builds the /device/selectAccounts URL with a PKCE
// challenge; the user authorizes in their browser; PollLogin polls
// /api/v1/deviceToken/poll with the verifier until the grant lands
// (404/202 = pending). Tokens: dt- (~30d) + drt- refresh (~1y), refreshed via
// POST /api/v1/deviceToken/refresh — no PAT involved anywhere.
// -----------------------------------------------------------------------------

const (
	qoderWebsiteCN   = "https://qoder.com.cn"
	qoderClientID    = "1c5e33e1-364d-4ce6-b02c-acaa81274a5c"
	qoderRedirectURI = "qoder-work-cn://"
)

// deviceTokenResponse mirrors /api/v1/deviceToken/{poll,refresh} payloads.
// expires_in / refresh_token_expires_in are MILLISECONDS (client main.js and
// the live Global sample agree).
type deviceTokenResponse struct {
	Token                 string `json:"token"`
	DeviceToken           string `json:"device_token"` // refresh path returns this key instead
	RefreshToken          string `json:"refresh_token"`
	UserID                string `json:"user_id"`
	ExpiresAt             string `json:"expires_at"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresAt string `json:"refresh_token_expires_at"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
}

func (d *deviceTokenResponse) accessToken() string {
	if d.Token != "" {
		return d.Token
	}
	return d.DeviceToken
}

// makePKCE returns (verifier, challenge) per RFC 7636 (S256).
func makePKCE() (string, string) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failure is fatal for PKCE; fall back to uuid material.
		s := uuid.NewString() + uuid.NewString()
		sum := sha256.Sum256([]byte(s))
		v := base64.RawURLEncoding.EncodeToString(sum[:])
		return v, v
	}
	verifier := make([]byte, 64)
	for i, b := range raw {
		verifier[i] = alphabet[int(b)%len(alphabet)]
	}
	sum := sha256.Sum256(verifier)
	return string(verifier), base64.RawURLEncoding.EncodeToString(sum[:])
}

// handleStartLogin implements AuthProvider.StartLogin: build the device
// authorization URL and stash the PKCE verifier under the returned state.
func handleStartLogin(raw []byte) ([]byte, error) {
	if globalBackendEnabled() {
		return handleGlobalStartLogin()
	}
	verifier, challenge := makePKCE()
	nonce := uuid.NewString()
	machineID := uuid.NewString()

	q := url.Values{}
	q.Set("challenge", challenge)
	q.Set("challenge_method", "S256")
	q.Set("nonce", nonce)
	q.Set("machine_id", machineID)
	q.Set("client_id", qoderClientID)
	q.Set("redirect_uri", qoderRedirectURI)
	authURL := qoderWebsiteCN + "/device/selectAccounts?" + q.Encode()

	now := time.Now()
	state := fmt.Sprintf("qw-%d", now.UnixNano())
	loginStates.Store(state, &loginCtx{verifier: verifier, nonce: nonce, expires: now.Add(loginTTL), startedAt: now.UnixNano()})
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       authURL,
		State:     state,
		ExpiresAt: now.Add(loginTTL).UTC(),
		Metadata: map[string]any{
			"logo":   pluginLogoURL,
			"prompt": "在打开的页面中登录并授权 QoderWork（设备授权，无需 PAT）。完成后此窗口会自动关闭。",
		},
	})
}

// pollDeviceToken performs one GET against /api/v1/deviceToken/poll.
// Returns (tok, pending, error): pending=true means the user hasn't finished
// authorizing yet (upstream 404/202) — the host should keep polling.
func pollDeviceToken(nonce, verifier string) (*deviceTokenResponse, bool, error) {
	q := url.Values{}
	q.Set("nonce", nonce)
	q.Set("verifier", verifier)
	q.Set("challenge_method", "S256")
	fullURL := upstreamBaseCN + "/api/v1/deviceToken/poll?" + q.Encode()

	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "QoderWork")
	resp, err := sharedHTTPClient().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusAccepted {
		return nil, true, nil
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("deviceToken poll: http %d body=%s", resp.StatusCode, truncateRedacted(string(raw), 200))
	}
	var out deviceTokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, fmt.Errorf("deviceToken poll parse: %w", err)
	}
	if out.accessToken() == "" {
		return nil, true, nil // 200 without token yet — treat as pending
	}
	return &out, false, nil
}

// refreshDeviceToken calls POST /api/v1/deviceToken/refresh with a drt-.
func refreshDeviceToken(drt string) (*deviceTokenResponse, error) {
	body, _ := json.Marshal(map[string]string{"refresh_token": drt})
	data, _, err := doRawJSON(sharedHTTPClient(), http.MethodPost, upstreamBaseCN+"/api/v1/deviceToken/refresh", nil, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out deviceTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("deviceToken refresh parse: %w", err)
	}
	if out.accessToken() == "" || out.RefreshToken == "" {
		return nil, fmt.Errorf("deviceToken refresh: incomplete token pair in response")
	}
	return &out, nil
}

// handlePollLogin implements AuthProvider.PollLogin. Three paths, in order:
//  1. Legacy PAT pasted into the modal (kept for backward compatibility).
//  2. Panel import produced a new auth file after StartLogin (unchanged).
//  3. Device-authorization poll (the real OAuth flow).
func handlePollLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		return nil, fmt.Errorf("poll: empty state")
	}
	v, ok := loginStates.Load(state)
	if !ok {
		return nil, fmt.Errorf("poll: unknown state (restart login)")
	}
	lc := v.(*loginCtx)
	if time.Now().After(lc.expires) {
		loginStates.Delete(state)
		return nil, fmt.Errorf("poll: login expired (10 min timeout)")
	}
	if globalBackendEnabled() {
		return handleGlobalPollLogin(state)
	}

	// Legacy path: user pasted PAT into the OAuth modal callback.
	pat := ""
	if req.Metadata != nil {
		if v, ok := req.Metadata["pat"].(string); ok {
			pat = strings.TrimSpace(v)
		}
	}
	if pat != "" {
		if !strings.HasPrefix(pat, "pt-") {
			return nil, fmt.Errorf("poll: PAT must start with pt-")
		}
		tok, err := exchangePATForJobToken(pat)
		if err != nil {
			return nil, fmt.Errorf("PAT exchange failed: %w", err)
		}
		ui, _ := fetchUserInfo(tok.Token)
		sa := buildStoredAuthFromJobToken(pat, tok, ui)
		loginStates.Delete(state)
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status: pluginapi.AuthLoginStatusSuccess,
			Auth:   toAuthData(sa),
		})
	}

	// Device-authorization path: poll the grant. The verifier never left
	// this process; the nonce pairs it to the auth URL we handed out.
	tok, pending, err := pollDeviceToken(lc.nonce, lc.verifier)
	if err != nil {
		return nil, fmt.Errorf("device authorization poll: %w", err)
	}
	if !pending && tok != nil {
		// Auth file must land IMMEDIATELY — no upstream calls before return.
		// The host writes the auth file from AuthLoginPollResponse, so any
		// blocking work (fetchUserInfo, hostAuthList) delays persistence.
		// UserID is already in the poll response; nickname is fetched lazily
		// by the panel on first load. PAT coalescing happens on keepalive.
		sa := buildStoredAuthFromDeviceToken(tok, nil)
		loginStates.Delete(state)
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status: pluginapi.AuthLoginStatusSuccess,
			Auth:   toAuthData(sa),
		})
	}

	// Panel-import path: a new qoderwork auth file appeared since StartLogin.
	files, err := hostAuthList()
	if err == nil {
		for _, f := range files {
			if f.CreatedAt.UnixNano() > lc.startedAt {
				sa, err := hostAuthGet(f.AuthIndex)
				if err == nil && sa != nil {
					loginStates.Delete(state)
					return okEnvelope(pluginapi.AuthLoginPollResponse{
						Status: pluginapi.AuthLoginStatusSuccess,
						Auth:   toAuthData(sa),
					})
				}
			}
		}
	}

	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusPending,
		Message: "等待浏览器完成 QoderWork 设备授权",
	})
}

// deviceExpiryUnix resolves the access-token expiry from a deviceToken
// poll/refresh response. Poll returns expires_in (ms); refresh returns only
// expires_at (RFC3339). Default: 30 days (observed dt- lifetime).
func deviceExpiryUnix(tok *deviceTokenResponse) int64 {
	if tok.ExpiresIn > 0 {
		return time.Now().Add(time.Duration(tok.ExpiresIn) * time.Millisecond).Unix()
	}
	if tok.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, tok.ExpiresAt); err == nil {
			return t.Unix()
		}
	}
	return time.Now().Add(30 * 24 * time.Hour).Unix()
}

// buildStoredAuthFromDeviceToken maps a device-token grant onto storedAuth.
// AccessToken=dt-, RefreshToken=drt-. When ui is nil (fast path during
// PollLogin), UserID comes from the poll response and nickname is empty —
// the panel fills it lazily on first load. preservePAT=false skips the
// hostAuthList scan to avoid blocking auth-file persistence.
func buildStoredAuthFromDeviceToken(tok *deviceTokenResponse, ui *userInfoResponse) *storedAuth {
	expiresAt := deviceExpiryUnix(tok)
	uid := tok.UserID
	nickname := ""
	if ui != nil {
		if ui.ID != "" {
			uid = ui.ID
		}
		nickname = ui.Name
	}
	// When ui is nil (fast PollLogin path), derive a readable nickname from
	// the uid so the panel doesn't fall back to the raw filename label.
	if nickname == "" {
		if len(uid) > 8 {
			nickname = "u" + uid[len(uid)-8:]
		} else {
			nickname = "u" + uid
		}
	}
	return &storedAuth{
		Auth: storedTokens{
			AccessToken:   tok.accessToken(),
			RefreshToken:  tok.RefreshToken,
			PersonalToken: "", // PAT coalescing happens on keepalive, not here
			ExpiresAt:     expiresAt,
			Domain:        "qoder.com.cn",
		},
		Account: storedAccount{UID: uid, Nickname: nickname},
	}
}

// existingPATForUID looks up an existing qoderwork auth file for the same
// uid and returns its stored PAT (empty when none). Lets OAuth re-login
// preserve a previously imported PAT instead of wiping it.
//
// NOTE: This calls hostAuthList which is a blocking host RPC. It must NOT
// be called during PollLogin (delays auth file persistence). It is safe to
// call during keepalive/refresh which happens long after the file is written.
func existingPATForUID(uid string) string {
	if uid == "" {
		return ""
	}
	files, err := hostAuthList()
	if err != nil {
		return ""
	}
	for _, f := range files {
		if !strings.HasPrefix(strings.ToLower(f.Name), providerName+"-") {
			continue
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil || sa == nil {
			continue
		}
		if sa.Account.UID == uid && strings.HasPrefix(sa.Auth.PersonalToken, "pt-") {
			return sa.Auth.PersonalToken
		}
	}
	return ""
}

// handleRefreshAuth implements AuthProvider.Refresh. The two credential
// families coexist in one auth file and are routed by TOKEN PREFIX, not by
// the presence of a PAT — an OAuth account may ALSO carry a PAT for fallback:
//
//  1. OAuth device family (refresh token starts with "drt-"): POST
//     /api/v1/deviceToken/refresh — the device flow's own endpoint.
//     The PAT (if any) is preserved untouched in the file.
//  2. Legacy PAT family (refresh token starts with "jrt-", or a bare PAT):
//     jrt- refresh → PAT re-exchange, as before.
//
// The previous PersonalToken-presence routing had a fatal flaw: once a PAT
// coexisted, a device account was force-refreshed through the jobToken
// endpoints, which reject drt- tokens, and the PAT fallback then OVERWROTE
// the OAuth credential with a jobToken pair — destroying the OAuth session.
func handleRefreshAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	if globalBackendEnabled() {
		// qodercli owns the real global credential and refresh lifecycle.
		// The CPA auth record is only a local routing marker.
		sa.Auth.ExpiresAt = time.Now().AddDate(10, 0, 0).Unix()
		return okEnvelope(pluginapi.AuthRefreshResponse{Auth: toAuthDataForRefresh(sa)})
	}

	if strings.HasPrefix(sa.Auth.RefreshToken, "drt-") {
		// OAuth device family — deviceToken/refresh ONLY.
		tok, err := refreshDeviceToken(sa.Auth.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("refresh rejected: %w — deviceToken refresh failed; re-login via OAuth required", err)
		}
		sa.Auth.AccessToken = tok.accessToken()
		sa.Auth.RefreshToken = tok.RefreshToken
		sa.Auth.ExpiresAt = preserveExpiry(deviceExpiryUnix(tok), sa.Auth.ExpiresAt)
		invalidateCosySession(sa.Account.UID)
		return okEnvelope(pluginapi.AuthRefreshResponse{Auth: toAuthDataForRefresh(sa)})
	}

	// Legacy PAT family: Tier 1 jrt- refresh → Tier 2 PAT re-exchange.
	tok, err := refreshJobToken(sa.Auth.RefreshToken)
	if err != nil && sa.Auth.PersonalToken != "" {
		// Tier 2: jrt- expired — fall back to PAT re-exchange.
		tok, err = exchangePATForJobToken(sa.Auth.PersonalToken)
	}
	if err != nil {
		return nil, fmt.Errorf("refresh rejected: %w — both jrt- refresh and PAT re-exchange failed; re-import PAT required", err)
	}
	sa.Auth.AccessToken = tok.Token
	if tok.RefreshToken != "" {
		sa.Auth.RefreshToken = tok.RefreshToken
	}
	sa.Auth.ExpiresAt = preserveExpiry(
		time.Now().Add(time.Duration(tok.ExpiresIn)*time.Millisecond).Unix(),
		sa.Auth.ExpiresAt,
	)
	invalidateCosySession(sa.Account.UID)
	// Host persists the refreshed credential itself after Refresh returns
	// (conductor.go refreshAuth → m.Update → persist). Writing from the
	// plugin too would double-write the file.
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: toAuthDataForRefresh(sa)})
}

// preserveExpiry reuses the previous token's expiresAt when the refresh
// response omits expiresIn. Zero would tell the host the credential is
// permanently expired and trigger a refresh storm on every request.
func preserveExpiry(newExpiry, oldExpiry int64) int64 {
	if newExpiry > 0 {
		return newExpiry
	}
	return oldExpiry
}

// toAuthDataForRefresh mirrors the workbuddy helper: blank out FileName and
// ID so the host backfills from the original auth path (prevents ID mismatch
// duplicate files when Refresh round-trips the record).
func toAuthDataForRefresh(sa *storedAuth) pluginapi.AuthData {
	ad := toAuthDataOpts(sa, nil, false)
	ad.FileName = "" // let host backfill original
	ad.ID = ""       // let host compute from path (prevents ID mismatch dupes)
	return ad
}
