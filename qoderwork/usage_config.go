// usage_config.go decodes plugin config from config_yaml on every
// register/reconfigure call and resolves the CPAMP usage report URL/key.
// All plugin-level config lives here so the rest of the plugin reads
// consistent, lock-protected snapshots.
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// check-in schedule: 09:00 and 21:00 local time.
var checkinHours = []int{9, 21}

// plugin-level config decoded from plugin.register/reconfigure config_yaml.
var (
	qoderBackend  = "cn"
	qoderCLIPath  = "qodercli"
	qoderConfigMu sync.RWMutex

	checkinAuto   = true // enabled by default
	checkinAutoMu sync.RWMutex

	// usageReportURL / usageReportKey: POST NDJSON to CPA-Manager-Plus
	// /v0/management/usage/import (only path that reaches request monitoring;
	// c-shared plugins cannot use host usage.DefaultManager/redisqueue).
	//
	// Resolution order (community-style, like codex-auth-importer env injection):
	//  1) plugins.configs.qoderwork.usage_report_* in config.yaml
	//  2) env USAGE_REPORT_URL / USAGE_REPORT_KEY / CPAMP_ADMIN_KEY
	//  3) secret files (docker secrets / bind-mount), e.g. /run/secrets/cpamp_admin_key
	// Default URL targets the compose service name of CPA-Manager-Plus.
	usageReportURL = defaultUsageReportURL
	usageReportKey = ""
	usageReportMu  sync.RWMutex

	// managementAPIKey: plugin-layer auth for /v0/management/plugins/qoderwork/*
	// write endpoints. When empty, plugin relies on host-side auth (CPA's
	// management middleware) — that's the historical default and stays
	// backward-compatible. When set via config_yaml management_key: or env
	// WB_MANAGEMENT_KEY, handleManagement enforces constant-time Bearer match
	// plus per-IP token-bucket rate limiting on mutating endpoints.
	managementAPIKey   = ""
	managementAPIKeyMu sync.RWMutex
)

// Default URL tries localhost first (works for both bare-metal and Docker
// host-network), falls back to Docker compose service name. The probe runs
// once at configure() time; a reachable endpoint wins.
//
// For users who run CPA Manager Plus on a different host/port, set
// usage_report_url in plugin config or env USAGE_REPORT_URL.
const defaultUsageReportURL = "http://127.0.0.1:18317/v0/management/usage/import"

const fallbackUsageReportURL = "http://cpa-manager-plus:18317/v0/management/usage/import"

// configure decodes plugin config from the lifecycle request.
func configure(raw []byte) {
	// Parse config without holding any lock (fixes nested-lock hazard).
	nextCheckinAuto := true
	nextLifecycleAuto := true
	nextSchedulerMode := schedulerModeOff // reset to default on reconfigure
	nextKeepaliveAuto := true
	nextMgmtKey := ""
	nextBackend := "cn"
	nextCLIPath := "qodercli"

	cfgURL, cfgKey := "", ""
	if len(raw) > 0 {
		var req struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &req); err == nil {
			for _, line := range strings.Split(string(req.ConfigYAML), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "checkin_auto:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "checkin_auto:"))
					nextCheckinAuto = v == "true" || v == "1" || v == "yes" || v == "on"
				}
				if strings.HasPrefix(line, "lifecycle_auto:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "lifecycle_auto:"))
					v = strings.Trim(v, "\"'")
					nextLifecycleAuto = v == "true" || v == "1" || v == "yes" || v == "on"
				}
				if strings.HasPrefix(line, "scheduler_mode:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "scheduler_mode:"))
					v = strings.Trim(v, "\"'")
					if v == schedulerModeCredits {
						nextSchedulerMode = schedulerModeCredits
					}
				}
				if strings.HasPrefix(line, "usage_report_url:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "usage_report_url:"))
					cfgURL = strings.Trim(v, "\"'")
				}
				if strings.HasPrefix(line, "usage_report_key:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "usage_report_key:"))
					cfgKey = strings.Trim(v, "\"'")
				}
				if strings.HasPrefix(line, "management_key:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "management_key:"))
					nextMgmtKey = strings.Trim(v, "\"'")
				}
				if strings.HasPrefix(line, "token_keepalive:") {
					v := strings.TrimSpace(strings.TrimPrefix(line, "token_keepalive:"))
					v = strings.Trim(v, "\"'")
					nextKeepaliveAuto = v == "true" || v == "1" || v == "yes" || v == "on"
				}
				if strings.HasPrefix(line, "backend:") {
					v := strings.ToLower(strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "backend:")), "\"'"))
					if v == "global" {
						nextBackend = "global"
					}
				}
				if strings.HasPrefix(line, "cli_path:") {
					v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "cli_path:")), "\"'")
					if v != "" {
						nextCLIPath = v
					}
				}
			}
		}
	}

	// The global backend delegates auth, refresh, and inference to qodercli.
	// China-only background jobs must not inspect its local marker credential.
	if nextBackend == "global" {
		nextCheckinAuto = false
		nextLifecycleAuto = false
		nextKeepaliveAuto = false
	}

	// Apply each setting under its own lock — no nesting.
	qoderConfigMu.Lock()
	qoderBackend = nextBackend
	qoderCLIPath = nextCLIPath
	qoderConfigMu.Unlock()

	checkinAutoMu.Lock()
	checkinAuto = nextCheckinAuto
	checkinAutoMu.Unlock()

	lifecycleAutoMu.Lock()
	lifecycleAuto = nextLifecycleAuto
	lifecycleAutoMu.Unlock()

	schedulerModeMu.Lock()
	schedulerMode = nextSchedulerMode
	schedulerModeMu.Unlock()

	keepaliveAutoMu.Lock()
	keepaliveAuto = nextKeepaliveAuto
	keepaliveAutoMu.Unlock()

	// management key: config_yaml > env > keep existing. Empty stays empty
	// (plugin-layer auth disabled, host middleware still guards).
	if nextMgmtKey == "" {
		nextMgmtKey = strings.TrimSpace(os.Getenv("WB_MANAGEMENT_KEY"))
	}
	managementAPIKeyMu.Lock()
	managementAPIKey = nextMgmtKey
	managementAPIKeyMu.Unlock()

	resolveUsageReport(cfgURL, cfgKey)
	ensureScheduler()
}

func globalBackendEnabled() bool {
	qoderConfigMu.RLock()
	defer qoderConfigMu.RUnlock()
	return qoderBackend == "global"
}

func configuredCLIPath() string {
	qoderConfigMu.RLock()
	defer qoderConfigMu.RUnlock()
	return qoderCLIPath
}

// resolveUsageReport fills usageReportURL/key from config → env → secret files.
// Mirrors community plugins that inject management keys via env/build (e.g.
// codex-auth-importer CODEX_AUTH_IMPORTER_MANAGEMENT_KEY), not plaintext CPA
// remote-management.secret-key (that field is bcrypt-hashed).
func resolveUsageReport(cfgURL, cfgKey string) {
	url := firstNonEmpty(
		strings.TrimSpace(cfgURL),
		strings.TrimSpace(os.Getenv("USAGE_REPORT_URL")),
		strings.TrimSpace(os.Getenv("CPAMP_USAGE_IMPORT_URL")),
	)
	if url == "" {
		url = probeUsageReportURL()
	}
	key := firstNonEmpty(
		strings.TrimSpace(cfgKey),
		strings.TrimSpace(os.Getenv("USAGE_REPORT_KEY")),
		strings.TrimSpace(os.Getenv("CPAMP_ADMIN_KEY")),
		strings.TrimSpace(os.Getenv("CPA_MANAGER_ADMIN_KEY")),
		readSecretFile(os.Getenv("USAGE_REPORT_KEY_FILE")),
		readSecretFile(os.Getenv("CPAMP_ADMIN_KEY_FILE")),
		readSecretFile(os.Getenv("CPA_MANAGER_ADMIN_KEY_FILE")),
		// docker compose secrets default path
		readSecretFile("/run/secrets/cpamp_admin_key"),
		readSecretFile("/run/secrets/cpamp-admin-key"),
		// optional bind-mounts used on this host
		readSecretFile("/CLIProxyAPI/secrets/cpamp-admin-key"),
		readSecretFile("/CLIProxyAPI/secrets/cpamp_admin_key"),
	)
	usageReportMu.Lock()
	usageReportURL = url
	usageReportKey = key
	usageReportMu.Unlock()
}

// probeUsageReportURL tries localhost first (bare-metal + Docker host-network),
// then Docker compose service name. Returns whichever responds; defaults to
// localhost if both fail (better to try localhost than an unreachable hostname).
func probeUsageReportURL() string {
	for _, candidate := range []string{defaultUsageReportURL, fallbackUsageReportURL} {
		if probeURL(candidate, 2*time.Second) {
			return candidate
		}
	}
	return defaultUsageReportURL
}

// probeURL does a quick HEAD/GET to check if the endpoint is reachable.
func probeURL(target string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(target)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Any HTTP response (even 401) means the endpoint is reachable;
	// connection refused / DNS failure means not reachable.
	return resp.StatusCode > 0
}

func readSecretFile(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
