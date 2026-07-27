// Package main implements the qoderwork CLIProxyAPI dynamic plugin.
//
// qoderwork wraps the QoderWork CN (qoder.com.cn) OpenAPI as a cliproxy
// provider: it exchanges a PAT for a jobToken, refreshes it, signs inference
// requests with COSY, and exposes the standard chat-completions interface.
// upstream /v2/chat/completions endpoint.
//
// This file is a clean-room reimplementation reconstructed from the public
// qoderwork.so binary (symbol table, string constants and RPC shape) published
// by Sliverkiss. Original credit for the qoderwork plugin goes to Sliverkiss;
// see https://github.com/Sliverkiss/cpa-plugin. Built with -buildmode=c-shared
// and exports the cliproxy C ABI entry points.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

// Wrappers so Go can invoke the host function-pointer table via cgo. The host
// API captured at init is used to push streaming chunks back asynchronously.
static int wb_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return api->call(api->host_ctx, method, request, request_len, response);
}
static void wb_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	api->free_buffer(ptr, len);
}

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	providerName  = "qoderwork"
	authFileName  = "qoderwork.json"
	pluginLogoURL = "https://raw.githubusercontent.com/DGZSbot/ai-icon/refs/heads/main/QoderWork.png"
	// QoderWork CN: OpenAPI for auth/billing, gateway for COSY-signed inference.
	// See /root/qoderwork/KNOWLEDGE.md §1-§5.
	upstreamBaseCN = "https://openapi.qoder.com.cn"
	gatewayBaseCN  = "https://gateway.qoder.com.cn"
	clientUA       = "Go-http-client/2.0"

	// Auth endpoints (PAT → jobToken exchange + refresh).
	endpointJobTokenExchange = upstreamBaseCN + "/api/v1/jobToken/exchange"
	endpointJobTokenRefresh  = upstreamBaseCN + "/api/v1/jobToken/refresh"

	// Business endpoints (jt- Bearer, no COSY).
	endpointUserInfo      = upstreamBaseCN + "/api/v1/userinfo"
	endpointQuotaUsage    = upstreamBaseCN + "/api/v2/quota/usage"
	endpointUserPlan      = upstreamBaseCN + "/api/v2/user/plan"
	endpointCheckinStatus = upstreamBaseCN + "/sash/api/v1/me/daily-check-in/status"
	endpointCheckinClaim  = upstreamBaseCN + "/sash/api/v1/me/daily-check-in/claim"
	endpointProUpgrade    = upstreamBaseCN + "/sash/api/v1/me/pro-upgrade/claim"

	// Inference endpoints (COSY-signed + QoderEncoding body).
	endpointChat   = gatewayBaseCN + "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	endpointModels = gatewayBaseCN + "/algo/api/v2/model/list?Encode=1"

	// loginTTL bounds one device-authorization flow. Users may need to log in
	// to qoder.com.cn first (Aliyun SSO) before authorizing — give them room.
	loginTTL = 10 * time.Minute
)

// loginCtx holds one in-flight device-authorization login flow. QoderWork's
// desktop clients use a PKCE device flow: the plugin generates
// verifier/challenge + nonce/machine_id, the user authorizes in a browser,
// and PollLogin exchanges the grant at /api/v1/deviceToken/poll.
type loginCtx struct {
	verifier  string // PKCE code_verifier — required by the poll endpoint
	nonce     string // device-flow nonce — paired with the auth URL
	expires   time.Time
	startedAt int64 // unix nano, set when StartLogin creates the state
}

var (
	hostAPI        *C.cliproxy_host_api // captured at init, used for async host calls
	loginStates    sync.Map             // state(string) -> *loginCtx
	httpClientOnce sync.Once
	sharedClient   *http.Client
)

// loginStatesPruneInterval bounds how often the janitor sweeps abandoned
// login states (user started a login but never finished).
const loginStatesPruneInterval = time.Minute

func init() {
	go func() {
		ticker := time.NewTicker(loginStatesPruneInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			loginStates.Range(func(key, value any) bool {
				if lc, ok := value.(*loginCtx); ok && now.After(lc.expires) {
					loginStates.Delete(key)
				}
				return true
			})
		}
	}()
}

func main() {}

// -----------------------------------------------------------------------------
// C ABI exports
// -----------------------------------------------------------------------------

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPI = host
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	// Intentionally a no-op. The host calls this on its own exit path (after
	// the host Go runtime has started tearing down) and dlclose()es this
	// library immediately afterwards. Touching any Go runtime state here —
	// mutexes, channel close, goroutine synchronization — risks a SIGSEGV in
	// cgo (observed on every docker restart: SIGSEGV in
	// _Cfunc_cliproxy_shutdown_plugin, PC near a freed runtime pointer).
	// The scheduler goroutine and janitor ticker hold no resources that
	// outlive the process; the OS reclaims them on exit.
}

// -----------------------------------------------------------------------------
// Host calls (async streaming + auth callbacks)
// -----------------------------------------------------------------------------

// hostCall invokes a host RPC method via the function-pointer table captured
// at init. Used to push stream chunks back asynchronously (host.stream.emit /
// host.stream.close) and to read the host's auth store (host.auth.list/get).
func hostCall(method string, request []byte) ([]byte, error) {
	if hostAPI == nil || hostAPI.call == nil {
		return nil, fmt.Errorf("host API unavailable")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cReq unsafe.Pointer
	var reqLen C.size_t
	if len(request) > 0 {
		cReq = C.CBytes(request)
		defer C.free(cReq)
		reqLen = C.size_t(len(request))
	}
	var resp C.cliproxy_buffer
	rc := C.wb_call_host(hostAPI, cMethod, (*C.uint8_t)(cReq), reqLen, &resp)
	var out []byte
	if resp.ptr != nil && resp.len > 0 {
		out = C.GoBytes(resp.ptr, C.int(resp.len))
	}
	if resp.ptr != nil && hostAPI.free_buffer != nil {
		C.wb_free_host_buffer(hostAPI, resp.ptr, resp.len)
	}
	if rc != 0 {
		return out, fmt.Errorf("host call %s returned %d", method, int(rc))
	}
	return out, nil
}

// -----------------------------------------------------------------------------

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		configure(request)
		return okEnvelope(wbRegistration())
	case pluginabi.MethodModelStatic:
		return handleModelStatic(request)
	case pluginabi.MethodModelForAuth:
		return handleModelForAuth(request)
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodAuthParse:
		return handleParseAuth(request)
	case pluginabi.MethodAuthLoginStart:
		return handleStartLogin(request)
	case pluginabi.MethodAuthLoginPoll:
		return handlePollLogin(request)
	case pluginabi.MethodAuthRefresh:
		return handleRefreshAuth(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodExecutorExecute:
		return handleExecExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecStream(request)
	case pluginabi.MethodExecutorCountTokens:
		// Upstream QoderWork has no dedicated count_tokens API. Return
		// unhandled-style zero estimate so clients fall back / skip.
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})
	case pluginabi.MethodManagementRegister:
		// Cache host-injected BasePath so handleManagement doesn't hardcode
		// /v0/management (v0.6.31: tolerate future host path changes).
		var regReq pluginapi.ManagementRegistrationRequest
		if err := json.Unmarshal(request, &regReq); err == nil {
			if regReq.BasePath != "" {
				setManagementBasePath(regReq.BasePath)
			}
		}
		return okEnvelope(managementRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	case pluginabi.MethodSchedulerPick:
		return handleSchedulerPick(request)
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// -----------------------------------------------------------------------------
// Registration & models
// -----------------------------------------------------------------------------

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	FrontendAuthProvider  bool                         `json:"frontend_auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	Scheduler             bool                         `json:"scheduler"`
	ManagementAPI         bool                         `json:"management_api"`
	UsagePlugin           bool                         `json:"usage_plugin"`
}

// version is injected at build time via -ldflags "-X main.version=...".
var version = "0.8.2"

func wbRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "Sliverkiss (based on qoderwork by lovingfish)",
			GitHubRepository: "https://github.com/Sliverkiss/cpa-plugin",
			Logo:             pluginLogoURL,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "backend", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"cn", "global"}, Description: "Qoder service: cn keeps the direct QoderWork China transport; global uses an authenticated local qodercli (default cn)."},
				{Name: "cli_path", Type: pluginapi.ConfigFieldTypeString, Description: "Global backend only: qodercli executable path or command name (default qodercli)."},
				{Name: "checkin_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable daily auto check-in at 09:00 and 21:00 local time for CN accounts (default true)."},
				{Name: "lifecycle_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Auto disable CN when credits exhausted; re-enable CN after check-in restores credits (default true)."},
				{Name: "token_keepalive", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable daily access-token refresh at 22:00 local time to prevent Keycloak offline-session expiry (default true)."},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Optional model list. Each item can have id, name, alias, context, max_tokens, enabled, reasoning."},
				{Name: "scheduler_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{schedulerModeOff, schedulerModeCredits}, Description: "Multi-account selection: off (defer to built-in, default) or credits (pick highest remaining). WARNING: when off + lifecycle_auto=false, exhausted accounts may still be routed — enable lifecycle_auto or set scheduler_mode=credits."},
				{Name: "usage_report_url", Type: pluginapi.ConfigFieldTypeString, Description: "Optional override of CPAMP usage import URL (default http://cpa-manager-plus:18317/v0/management/usage/import; also env USAGE_REPORT_URL)."},
				{Name: "usage_report_key", Type: pluginapi.ConfigFieldTypeString, Description: "Optional CPAMP admin key override. Prefer auto-detect from env CPAMP_ADMIN_KEY / USAGE_REPORT_KEY or secret file /run/secrets/cpamp_admin_key."},
			},
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			FrontendAuthProvider:  false,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			ManagementAPI:         true,
			Scheduler:             true,
			UsagePlugin:           true,
		},
	}
}

// dynamicModelsCacheTTL bounds how long a fetched model list is reused.
// model.static / model.for_auth are re-invoked by CPA on every config reload
// and on each models query; without caching, every reload fans out to one
// upstream call per account.
const dynamicModelsCacheTTL = 5 * time.Minute

var dynamicModelsCache struct {
	sync.RWMutex
	models  []pluginapi.ModelInfo
	fetched time.Time
}

//
// CPA applies oauth-model-alias to the models this plugin registers, so the
// gateway may route a request whose model ID is an alias (e.g.
// "point/deepseek-v4-flash") to this executor. The upstream only knows the
// real model IDs, so the plugin must map the alias back before forwarding.
//
// ExecutorRequest carries no host config, so the alias table is cached from
// the AuthModelRequest.Host summary every time the host asks for models
// (model.static / model.for_auth are re-queried by CPA on config reload,
// keeping this cache in sync with oauth-model-alias changes). Auth-level
// attribute overrides ("model_alias"/"model-alias"/"oauth-model-alias")
// are parsed per request and take precedence over the global table.

var modelAliasCache struct {
	sync.RWMutex
	byAlias map[string]string
}

// ------------------------------------------------------------------------------
// Usage reporting (request monitoring)
// ------------------------------------------------------------------------------
//
// CPA built-in executors publish via host usage.DefaultManager → redisqueue.
// Plugin executors cannot: c-shared .so has its own Go runtime, so
// usage.PublishRecord would hit a separate empty DefaultManager (no sink).
//
// Only effective path: POST NDJSON to CPA-Manager-Plus
// /v0/management/usage/import. Key/URL resolved automatically from
// config → env → docker secret files (see resolveUsageReport).
// usage.Detail is still used as a pure token-counter struct.

func hostAuthListFiles() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.list failed")
	}
	var resp rpcHostAuthListResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	return resp.Files, nil
}

// hostAuthGetByIndex fetches the raw JSON for one auth index.
func hostAuthGetByIndex(authIndex string) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	raw, err := hostCall(pluginabi.MethodHostAuthGet, body)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.get failed")
	}
	var resp rpcHostAuthGetResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	return resp.JSON, nil
}

// storedAuth is the on-disk shape of a qoderwork credential.
type storedAuth struct {
	Auth    storedTokens  `json:"auth"`
	Account storedAccount `json:"account"`
}

// storedTokens holds one credential family at a time in Access/RefreshToken,
// plus the long-lived PAT fallback. Two families coexist in one file:
//
//	PAT family:   accessToken=jt- (24h),  refreshToken=jrt- (48h)
//	OAuth family: accessToken=dt- (~30d), refreshToken=drt- (~1y, rotating)
//
// personalToken (pt-) is family-independent and never overwritten by refreshes
// — it re-exchanges a fresh jobToken pair when both live tokens die.
type storedTokens struct {
	AccessToken   string `json:"accessToken"`   // jt- (24h) or dt- (~30d)
	RefreshToken  string `json:"refreshToken"`  // jrt- (48h) or drt- (~1y)
	PersonalToken string `json:"personalToken"` // pt-..., long-lived fallback
	ExpiresAt     int64  `json:"expiresAt"`     // active-token expiry (unix seconds)
	Domain        string `json:"domain"`        // realm: qoder.com.cn
}

type storedAccount struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

// apiEnvelope is the generic {code,msg,data} wrapper used by every QoderWork API.
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// jobTokenResponse is defined in oauth.go; keepalive and handleRefreshAuth
// both use it. The old tokenData struct (camelCase tags) was wrong and has
// been removed — QoderWork returns snake_case JSON.

func parseStored(raw []byte) (*storedAuth, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty auth storage")
	}
	// Accept both shapes seen in the wild:
	//   nested: {"auth":{"accessToken":...},"account":{"uid":...}} (plugin/oauth output)
	//   flat:   {"accessToken":...,"uid":...,"nickname":...} (CPA-Manager-Plus auths/qoderwork.json)
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("storage_parse_error: %w", err)
	}
	var sa storedAuth
	if _, nested := probe["auth"]; nested {
		if err := json.Unmarshal(raw, &sa); err != nil {
			return nil, fmt.Errorf("storage_parse_error: %w", err)
		}
	} else {
		var flat struct {
			AccessToken   string `json:"accessToken"`
			RefreshToken  string `json:"refreshToken"`
			PersonalToken string `json:"personalToken"` // PAT fallback — must survive the flat shape too
			ExpiresAt     int64  `json:"expiresAt"`
			Domain        string `json:"domain"`
			UID           string `json:"uid"`
			EnterpriseID  string `json:"enterpriseId"`
			Nickname      string `json:"nickname"`
		}
		if err := json.Unmarshal(raw, &flat); err != nil {
			return nil, fmt.Errorf("storage_parse_error: %w", err)
		}
		sa.Auth = storedTokens{AccessToken: flat.AccessToken, RefreshToken: flat.RefreshToken, PersonalToken: flat.PersonalToken, ExpiresAt: flat.ExpiresAt, Domain: flat.Domain}
		sa.Account = storedAccount{UID: flat.UID, EnterpriseID: flat.EnterpriseID, Nickname: flat.Nickname}
	}
	if sa.Auth.AccessToken == "" {
		return nil, fmt.Errorf("parse_error: missing accessToken")
	}
	return &sa, nil
}

// -------------------------------------------------------------------------------
// HTTP plumbing
// -------------------------------------------------------------------------------

func commonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", clientUA)
}

// applyCosyHeaders computes the full COSY-signed header set for one inference
// request and applies it to req. Body must be the QoderEncoding-encoded
// request body (string form), url the gateway endpoint, modelKey the
// upstream model key (goes into x-model-key). sse toggles cache-control.
//
// Returns an error if the cosy session cannot be built (e.g. empty token).
func applyCosyHeaders(req *http.Request, sa *storedAuth, encodedBody, rawURL, modelKey string, sse bool) error {
	sess, err := cosySessionFor(sa)
	if err != nil {
		return err
	}
	hdr, err := sess.headers(sa.Account.UID, encodedBody, rawURL, "text/event-stream", sse)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if modelKey != "" {
		req.Header.Set("x-model-key", modelKey)
		req.Header.Set("x-model-source", "system")
	}
	return nil
}

// -----------------------------------------------------------------------------
// Auth handlers
// -----------------------------------------------------------------------------

func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// Ownership check (CPA native contract): the host routes by the file's
	// top-level "type" field (synthesizer/file.go). Files without a type fall
	// back to polling every plugin — first Handled=true wins. To prevent
	// claiming foreign providers' legacy files (e.g. workbuddy's type-less
	// auths, which parseStored would otherwise accept because the nested
	// {auth,account} shape is identical), only claim files whose declared
	// type matches us — or whose filename carries our prefix.
	var probeType struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(req.RawJSON, &probeType)
	declared := strings.ToLower(strings.TrimSpace(probeType.Type))
	if declared != "" && declared != providerName {
		// Explicitly another provider's file — never claim it.
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if declared == "" {
		// No type declared: only claim when the host already routed this to us
		// (req.Provider == qoderwork) or the filename carries our prefix.
		routed := strings.EqualFold(strings.TrimSpace(req.Provider), providerName)
		prefixed := strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.FileName)), providerName+"-")
		if !routed && !prefixed {
			return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
		}
	}
	sa, err := parseStored(req.RawJSON)
	if err != nil {
		// Not a qoderwork credential; let the host try other providers.
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	isGlobalAuth := strings.EqualFold(strings.TrimSpace(sa.Auth.Domain), "qoder.com")
	if isGlobalAuth != globalBackendEnabled() {
		// Keep credentials isolated when an installation switches backend.
		// In particular, never route the global CLI marker into CN HTTP code.
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	// CRITICAL: echo back the host-provided FileName AND leave ID empty.
	//
	// CPA uses ID for auth record identity (upsert key). If we set ID=uid
	// while the host's watcher initially registered with ID=filename,
	// upsertAuthRecord can't find the existing record → creates a NEW one
	// → duplicate auth entries (same file, different IDs).
	//
	// By leaving ID empty, CPA falls back to authIDForPath(path) which
	// derives ID from the file path → always matches the watcher's key.
	// FileName is also echoed back to avoid rename-based duplicates.
	ad := toAuthDataOpts(sa, nil, false)
	ad.ID = "" // let host compute from path (prevents ID mismatch dupes)
	if fn := strings.TrimSpace(req.FileName); fn != "" {
		ad.FileName = fn
	}
	return okEnvelope(pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    ad,
	})
}

func toAuthData(sa *storedAuth) pluginapi.AuthData {
	return toAuthDataOpts(sa, nil, false)
}

// toAuthDataOpts builds AuthData with optional credits snapshot and disabled flag.
func toAuthDataOpts(sa *storedAuth, cr *creditsSummary, disabled bool) pluginapi.AuthData {
	storage, _ := json.Marshal(sa)
	id := providerName
	fileName := authFileName
	if sa != nil {
		if uid := sanitizeUIDForFileName(sa.Account.UID); uid != "" {
			id = uid
			fileName = "qoderwork-" + uid + ".json"
		}
	}
	label := labelForAuth(sa)
	meta := enrichAuthMetadata(sa, cr, disabled)
	return pluginapi.AuthData{
		Provider:    providerName,
		ID:          id,
		FileName:    fileName,
		Label:       label,
		Disabled:    disabled,
		StorageJSON: storage,
		// Standardized auth metadata. `type` is required by the host for
		// auth-file classification; `logo`/`note`/`disabled` surface on auth rows.
		Metadata: meta,
	}
}

// -----------------------------------------------------------------------------

func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if globalBackendEnabled() {
		return handleGlobalExecute(req)
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	// Map CPA-facing model name (e.g. "qoder/qmodel_preview" or "qmodel_preview")
	// to the upstream key the gateway recognises.
	upstreamModel := cpaToUpstreamKey(stripProviderPrefix(req.Model))
	started := time.Now()
	authUID := ""
	if sa.Account.UID != "" {
		authUID = sa.Account.UID
	}
	// Build the QoderWork agent_chat_generation body from the OpenAI request,
	// then QoderEncoding-encode it. The template embeds a 10657-token system
	// prompt that the server requires for normal behaviour (KNOWLEDGE §5.2).
	qwReq := &openAIRequest{}
	if err := json.Unmarshal(req.Payload, qwReq); err != nil && len(req.Payload) > 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "payload parse: "+err.Error())
		return nil, fmt.Errorf("payload parse: %w", err)
	}
	body, err := buildQoderBody(qwReq, upstreamModel, uiUserType(nil))
	if err != nil {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "body build: "+err.Error())
		return nil, fmt.Errorf("body build: %w", err)
	}
	encodedBody := qoderEncode(body)
	httpReq, err := http.NewRequest(http.MethodPost, endpointChat, strings.NewReader(encodedBody))
	if err != nil {
		return nil, err
	}
	if err := applyCosyHeaders(httpReq, sa, encodedBody, endpointChat, upstreamModel, true); err != nil {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "cosy: "+err.Error())
		return nil, fmt.Errorf("cosy: %w", err)
	}
	// Compliance: route via host.http.do_stream so request-log captures the
	// outbound call. Read entire body via the bridge, then fold SSE → completion.
	stream, statusCode, _, err := hostHTTPDoStream(httpReq)
	if err != nil {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, err.Error())
		return nil, fmt.Errorf("http_error: %w", err)
	}
	defer stream.Close()
	reader := newHostStreamReader(stream)
	if statusCode >= 400 {
		payload, _ := io.ReadAll(reader)
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, string(payload))
		reconcileAfterExecutorError(req.AuthID, statusCode, string(payload))
		return nil, fmt.Errorf("upstream %d: %s", statusCode, truncateRedacted(string(payload), 200))
	}
	completion, err := aggregateQoderSSE(reader, req.Model)
	if err != nil {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, err.Error())
		return nil, err
	}
	publishUsage(req.Model, upstreamModel, authUID, started, usageDetailFromCompletion(completion), false, 0, "")
	invalidateAccountCredits(req.AuthID, authUID)
	return okEnvelope(pluginapi.ExecutorResponse{Payload: completion})
}

// stripProviderPrefix removes the leading "qoder/" (or any "<provider>/")
// segment from a CPA-facing model name, leaving the bare alias/key.
func stripProviderPrefix(model string) string {
	if i := strings.Index(model, "/"); i > 0 {
		return model[i+1:]
	}
	return model
}

// executorStreamRequest wraps the host's executor.execute_stream RPC: the
// ExecutorRequest plus the async stream id the host uses to receive chunks.
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func handleExecStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if globalBackendEnabled() {
		return handleGlobalStream(req)
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	upstreamModel := cpaToUpstreamKey(stripProviderPrefix(req.Model))
	started := time.Now()
	authUID := ""
	if sa.Account.UID != "" {
		authUID = sa.Account.UID
	}

	// Build the QoderWork body (template-based) and QoderEncoding-encode.
	bodyRaw := req.Payload
	if len(bodyRaw) == 0 {
		bodyRaw = req.OriginalRequest
	}
	qwReq := &openAIRequest{}
	if err := json.Unmarshal(bodyRaw, qwReq); err != nil && len(bodyRaw) > 0 {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "payload parse: "+err.Error())
		return nil, fmt.Errorf("payload parse: %w", err)
	}
	body, err := buildQoderBody(qwReq, upstreamModel, uiUserType(nil))
	if err != nil {
		publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, 0, "body build: "+err.Error())
		return nil, fmt.Errorf("body build: %w", err)
	}
	encodedBody := qoderEncode(body)

	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)

	// No async stream id → fall back to synchronous chunk collection.
	if req.StreamID == "" {
		collector := &sseUsageCollector{}
		chunks, statusCode, errCollect := collectUpstreamStreamQoder(encodedBody, sa, upstreamModel, sseFramed, collector)
		if errCollect != nil {
			publishUsage(req.Model, upstreamModel, authUID, started, usage.Detail{}, true, statusCode, errCollect.Error())
			return nil, errCollect
		}
		publishUsage(req.Model, upstreamModel, authUID, started, collector.detail(), false, 0, "")
		invalidateAccountCredits(req.AuthID, authUID)
		return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
	}

	// Async: return immediately with empty chunks. A goroutine pumps the upstream
	// and emits each chunk via host.stream.emit so the client sees true streaming.
	// Use context.Background() (not nil) so the request can be cancelled when the
	// client disconnects — otherwise the pump keeps reading a dead upstream until
	// sharedHTTPClient's 120s timeout, holding a pool slot the whole time.
	ctx, cancel := context.WithCancel(context.Background())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointChat, strings.NewReader(encodedBody))
	if err != nil {
		cancel()
		streamEmitError(req.StreamID, err.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: headers})
	}
	if err := applyCosyHeaders(httpReq, sa, encodedBody, endpointChat, upstreamModel, true); err != nil {
		cancel()
		streamEmitError(req.StreamID, "cosy: "+err.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: headers})
	}
	go pumpUpstreamStream(httpReq, cancel, req.StreamID, sseFramed, req.Model, upstreamModel, authUID, started, req.AuthID)
	return okEnvelope(streamResponse{Headers: headers})
}

// -----------------------------------------------------------------------------

func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
