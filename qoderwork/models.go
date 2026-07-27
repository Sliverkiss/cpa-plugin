// models.go implements the ModelProvider capability: static and per-auth
// model lists, dynamic model discovery via the upstream models API, alias
// reverse resolution (client-facing alias → upstream model id), and the
// host-config oauth-excluded-models filter.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// wbModels is the static fallback model list for QoderWork CN. Keys mirror
// /root/qoderwork/models_list.json (KNOWLEDGE §6.2). Aliases use the qoder/
// prefix in AuthAttributes; bare IDs work too. Dynamic refresh via
// /algo/api/v2/model/list replaces this at runtime when an account is present.
func wbModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		{ID: "auto", Name: "Auto", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qmodel_preview", Name: "Qwen3.8-Max-Preview", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qmodel_latest", Name: "Qwen3.7-Max", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "qmodel", Name: "Qwen3.7-Plus", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "q36fmodel", Name: "Qwen3.6-Flash", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "dmodel", Name: "DeepSeek-V4-Pro", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "dfmodel", Name: "DeepSeek-V4-Flash", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gm51model", Name: "GLM-5.2", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kmodel", Name: "Kimi-K2.7-Code", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "mmodel", Name: "MiniMax-M2.7", ContextLength: 180000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
	}
}

func cachedDynamicModels() ([]pluginapi.ModelInfo, bool) {
	dynamicModelsCache.RLock()
	defer dynamicModelsCache.RUnlock()
	if len(dynamicModelsCache.models) > 0 && time.Since(dynamicModelsCache.fetched) < dynamicModelsCacheTTL {
		return dynamicModelsCache.models, true
	}
	return nil, false
}

func storeDynamicModels(models []pluginapi.ModelInfo) {
	dynamicModelsCache.Lock()
	dynamicModelsCache.models = models
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.Unlock()
}

func fetchDynamicModels() []pluginapi.ModelInfo {
	if globalBackendEnabled() {
		return wbModels()
	}
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	models := wbModels()
	files, err := hostAuthListFiles()
	if err != nil || len(files) == 0 {
		return models
	}
	// Strict filename-prefix match — same filter as host_auth.go hostAuthList.
	// (Earlier code also matched files containing "codebuddy" anywhere, which
	// would wrongly include workbuddy-*.json auths here and cause us to call
	// the qoderwork models API with a workbuddy token.)
	prefix := providerName + "-"
	for _, f := range files {
		if !strings.HasPrefix(strings.ToLower(f.Name), prefix) {
			continue
		}
		raw, err := hostAuthGetByIndex(f.AuthIndex)
		if err != nil {
			continue
		}
		sa, err := parseStored(raw)
		if err != nil || sa == nil {
			continue
		}
		dyn, err := callModelsAPI(sa)
		if err == nil && len(dyn) > 0 {
			storeDynamicModels(dyn)
			return dyn
		}
	}
	return models
}

func fetchDynamicModelsFromStorage(storageJSON []byte) []pluginapi.ModelInfo {
	if globalBackendEnabled() {
		return wbModels()
	}
	if models, ok := cachedDynamicModels(); ok {
		return models
	}
	sa, err := parseStored(storageJSON)
	if err != nil || sa == nil {
		return fetchDynamicModels()
	}
	if dyn, err := callModelsAPI(sa); err == nil && len(dyn) > 0 {
		storeDynamicModels(dyn)
		return dyn
	}
	return fetchDynamicModels()
}

// fetchDynamicModels calls the QoderWork API to get the latest model list.
// Falls back to the hardcoded list on any error.
// callModelsAPI GETs /algo/api/v2/model/list from the QoderWork gateway
// with COSY signing (same as inference). Returns plain JSON (not QoderEncoding).
// Falls back to wbModels() on any error.
func callModelsAPI(sa *storedAuth) ([]pluginapi.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// model/list needs no body but COSY still requires a body string for signing.
	// An empty JSON object works (verified in reference_impl.py).
	encodedBody := qoderEncode([]byte("{}"))
	rawURL := endpointModels // includes ?Encode=1
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// COSY signing needs the encoded body even for GET (signature includes body).
	if err := applyCosyHeaders(req, sa, encodedBody, rawURL, "", false); err != nil {
		return nil, fmt.Errorf("cosy sign: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", clientUA)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models API status %d", resp.StatusCode)
	}
	// Response is plain JSON: {"chat":[{key,display_name,...}], "developer":[...], ...}
	var apiResp map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &apiResp); err != nil {
		return nil, fmt.Errorf("models parse: %w", err)
	}
	// Prefer the "chat" scene (matches our inference use case).
	chatRaw, ok := apiResp["chat"]
	if !ok {
		return nil, fmt.Errorf("no chat scene in models response")
	}
	var models []struct {
		Key            string  `json:"key"`
		DisplayName    string  `json:"display_name"`
		Enable         bool    `json:"enable"`
		IsReasoning    bool    `json:"is_reasoning"`
		IsVL           bool    `json:"is_vl"`
		MaxInputTokens int64   `json:"max_input_tokens"`
		PriceFactor    float64 `json:"price_factor"`
	}
	if err := json.Unmarshal(chatRaw, &models); err != nil {
		return nil, fmt.Errorf("chat scene parse: %w", err)
	}
	var out []pluginapi.ModelInfo
	for _, m := range models {
		if !m.Enable {
			continue
		}
		ctx2 := int64(180000)
		if m.MaxInputTokens > 0 {
			ctx2 = m.MaxInputTokens
		}
		out = append(out, pluginapi.ModelInfo{
			ID:                         m.Key,
			Name:                       m.DisplayName,
			ContextLength:              ctx2,
			MaxCompletionTokens:        8192,
			OwnedBy:                    providerName,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no enabled chat models")
	}
	return out, nil
}

func cacheModelAliases(host pluginapi.HostConfigSummary) {
	entries := host.OAuthModelAlias[providerName]
	if len(entries) == 0 {
		// Host may key the channel case-insensitively; fall back to a scan.
		for channel, list := range host.OAuthModelAlias {
			if strings.EqualFold(strings.TrimSpace(channel), providerName) {
				entries = list
				break
			}
		}
	}
	byAlias := make(map[string]string, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		alias := strings.TrimSpace(e.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		byAlias[strings.ToLower(alias)] = name
	}
	modelAliasCache.Lock()
	modelAliasCache.byAlias = byAlias
	modelAliasCache.Unlock()
}

// resolveUpstreamModel maps an aliased requested model back to the real
// upstream model ID. Returns the input unchanged when nothing matches.
func resolveUpstreamModel(model string, attributes map[string]string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return model
	}
	key := strings.ToLower(m)
	if name, ok := parseModelAliasAttribute(attributes)[key]; ok {
		return name
	}
	modelAliasCache.RLock()
	name, ok := modelAliasCache.byAlias[key]
	modelAliasCache.RUnlock()
	if ok {
		return name
	}
	return m
}

// parseModelAliasAttribute decodes a per-auth alias override from auth
// attributes. Accepts JSON ([{"name":...,"alias":...}] or {alias:name}) or
// comma-separated "alias=name" pairs.
func parseModelAliasAttribute(attributes map[string]string) map[string]string {
	if len(attributes) == 0 {
		return nil
	}
	raw := ""
	for _, k := range []string{"model_alias", "model-alias", "oauth-model-alias"} {
		if v := strings.TrimSpace(attributes[k]); v != "" {
			raw = v
			break
		}
	}
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	add := func(name, alias string) {
		name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
		if name != "" && alias != "" && !strings.EqualFold(name, alias) {
			out[strings.ToLower(alias)] = name
		}
	}
	if strings.HasPrefix(raw, "[") {
		var list []struct {
			Name  string `json:"name"`
			Alias string `json:"alias"`
		}
		if json.Unmarshal([]byte(raw), &list) == nil {
			for _, e := range list {
				add(e.Name, e.Alias)
			}
			return out
		}
	}
	if strings.HasPrefix(raw, "{") {
		var m map[string]string
		if json.Unmarshal([]byte(raw), &m) == nil {
			for alias, name := range m {
				add(name, alias)
			}
			return out
		}
	}
	for _, pair := range strings.Split(raw, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			add(kv[1], kv[0])
		}
	}
	return out
}

// filterExcludedModels removes models listed in oauth-excluded-models for
// the qoderwork provider. The host passes this config via HostConfigSummary.
func filterExcludedModels(models []pluginapi.ModelInfo, host pluginapi.HostConfigSummary) []pluginapi.ModelInfo {
	if len(host.ExcludedModels) == 0 {
		return models
	}
	// Try exact provider match, then case-insensitive scan.
	excluded := host.ExcludedModels[providerName]
	if len(excluded) == 0 {
		for channel, list := range host.ExcludedModels {
			if strings.EqualFold(strings.TrimSpace(channel), providerName) {
				excluded = list
				break
			}
		}
	}
	if len(excluded) == 0 {
		return models
	}
	excludeSet := make(map[string]struct{}, len(excluded))
	for _, m := range excluded {
		excludeSet[strings.ToLower(strings.TrimSpace(m))] = struct{}{}
	}
	// Use a fresh slice — models[:0] would alias the input's backing array,
	// which may be the dynamicModelsCache's own slice. Mutating it in place
	// would corrupt the cache for subsequent callers (P0 bug: after one
	// filterExcludedModels call, cache returns the filtered list as the
	// "full" list on the next fetch).
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if _, skip := excludeSet[strings.ToLower(m.ID)]; skip {
			continue
		}
		out = append(out, m)
	}
	return out
}

// publishUsage reports one upstream attempt into CPAMP request monitoring.
// requestedModel is client-facing (may be alias); upstreamModel is resolved.

func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cacheModelAliases(req.Host)
	models := fetchDynamicModels()
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

func handleModelForAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// Always return the plugin's canonical provider key. The host skips any
	// response whose Provider doesn't match the auth's provider, so echoing
	// req.AuthProvider back would silently drop the model list whenever the
	// auth file carries a non-canonical provider string.
	cacheModelAliases(req.Host)
	models := fetchDynamicModelsFromStorage(req.StorageJSON)
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}
