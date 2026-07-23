// lifecycle.go implements credit-based auth lifecycle for workbuddy:
//   - CN exhausted  → disable auth file (disabled:true), re-enable after check-in when credits return
//   - Global exhausted → delete auth file (one-shot quota)
//   - Unknown credits → no-op (never mis-kill)
//   - Hard credit errors from executor → recheck credits then apply policy
//   - Soft rate limits → do not delete Global
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// lifecycleAction is the policy decision for one account.
type lifecycleAction int

const (
	lifecycleNone lifecycleAction = iota
	lifecycleDisable
	lifecycleDelete
	lifecycleReenable
)

func (a lifecycleAction) String() string {
	switch a {
	case lifecycleDisable:
		return "disable"
	case lifecycleDelete:
		return "delete"
	case lifecycleReenable:
		return "reenable"
	default:
		return "none"
	}
}

// lifecycleAuto gates automatic disable/delete/reenable. Default true.
var (
	lifecycleAuto   = true
	lifecycleAutoMu sync.RWMutex
)

func lifecycleEnabled() bool {
	lifecycleAutoMu.RLock()
	defer lifecycleAutoMu.RUnlock()
	return lifecycleAuto
}

// shouldActOnCredits is true only when credits are *known* exhausted.
// nil / empty (no packages, no used) is unknown → false.
func shouldActOnCredits(cr *creditsSummary) bool {
	return isCreditsExhausted(cr)
}

// hardCreditMarkers are case-insensitive substrings in upstream error bodies.
var hardCreditMarkers = []string{
	"insufficient credit",
	"insufficient credits",
	"no credit",
	"no credits",
	"credit exhausted",
	"credits exhausted",
	"out of credit",
	"out of credits",
	"quota exceeded",
	"quota exhaust",
	"payment required",
	"积分不足",
	"额度不足",
	"余额不足",
	"积分用完",
	"额度用尽",
	"没有积分",
	"credit not enough",
	"not enough credit",
}

// isHardCreditError reports business "out of credits" style failures.
// 402 is treated as payment/credit. Pure 429 is not hard unless body has credit markers.
func isHardCreditError(status int, body string) bool {
	if status == httpStatusPaymentRequired {
		return true
	}
	lower := strings.ToLower(body)
	for _, m := range hardCreditMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	// Chinese markers may not lower-map usefully; also scan raw.
	for _, m := range hardCreditMarkers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

const httpStatusPaymentRequired = 402

// isSoftRateLimit is pure throttling without hard-credit semantics.
func isSoftRateLimit(status int, body string) bool {
	if isHardCreditError(status, body) {
		return false
	}
	if status == 429 {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "throttl")
}

// lifecycleActionFor chooses disable/delete/none from region + credits.
// Does not consider reenable (that needs disabled flag).
func lifecycleActionFor(region string, cr *creditsSummary) lifecycleAction {
	if !shouldActOnCredits(cr) {
		return lifecycleNone
	}
	if region == "global" {
		return lifecycleDelete
	}
	return lifecycleDisable
}

// shouldReenableCN is true when a CN account is disabled but now has credits.
func shouldReenableCN(disabled bool, cr *creditsSummary) bool {
	if !disabled {
		return false
	}
	if cr == nil {
		return false
	}
	if isCreditsExhausted(cr) {
		return false
	}
	// Known positive remain, or non-exhausted with packages still having room.
	return cr.TotalRemain > 0
}

// displayNote builds a one-line note for CPAMP Auth cards.
func displayNote(sa *storedAuth, cr *creditsSummary, disabled bool) string {
	region := strings.ToUpper(accountRegion(sa))
	if region == "CN" {
		region = "CN"
	} else {
		region = "Global"
	}
	parts := []string{region}
	if disabled {
		parts = append(parts, "已禁用")
	}
	if cr == nil {
		parts = append(parts, "积分未知")
	} else if isCreditsExhausted(cr) {
		parts = append(parts, fmt.Sprintf("耗尽 · 余%d 已用%d", cr.TotalRemain, cr.TotalUsed))
	} else {
		// Show remain as primary (what you can still spend). Used is real cycle spend.
		// Size (capacity) grows with check-in packs — do not treat size↑ as usage↓.
		if cr.TotalSize > 0 {
			parts = append(parts, fmt.Sprintf("余%d 已用%d 池%d", cr.TotalRemain, cr.TotalUsed, cr.TotalSize))
		} else {
			parts = append(parts, fmt.Sprintf("余%d 已用%d", cr.TotalRemain, cr.TotalUsed))
		}
	}
	note := strings.Join(parts, " · ")
	if len(note) > 80 {
		note = note[:77] + "..."
	}
	return note
}

// labelForAuth adds [CN]/[Global] for host labels.
func labelForAuth(sa *storedAuth) string {
	base := "WorkBuddy"
	if sa != nil && strings.TrimSpace(sa.Account.Nickname) != "" {
		base = strings.TrimSpace(sa.Account.Nickname)
	}
	tag := "CN"
	if accountRegion(sa) == "global" {
		tag = "Global"
	}
	return base + " [" + tag + "]"
}

// authFileNameFor matches toAuthData naming: always workbuddy-<uid>.json when UID is known.
// Bare "workbuddy.json" is legacy single-account only (no UID).
func authFileNameFor(sa *storedAuth) string {
	if sa != nil && strings.TrimSpace(sa.Account.UID) != "" {
		return "workbuddy-" + strings.TrimSpace(sa.Account.UID) + ".json"
	}
	return authFileName
}

// isLegacyWorkbuddyAuthName reports the historical single-file name that collides
// with multi-account workbuddy-<uid>.json for the same credential.
func isLegacyWorkbuddyAuthName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), authFileName)
}

// resolveAuthFileTarget picks the canonical file name + path for save/delete.
// Prefer workbuddy-<uid>.json; if the host still points at legacy workbuddy.json
// for a UID-bearing account, rewrite to the uid name and schedule legacy removal.
func resolveAuthFileTarget(sa *storedAuth, phys *hostAuthPhysical) (name, path string, legacyPath string) {
	name = authFileNameFor(sa)
	if phys != nil {
		path = strings.TrimSpace(phys.Path)
		physName := strings.TrimSpace(phys.Name)
		if physName != "" && !isLegacyWorkbuddyAuthName(physName) {
			// Already on multi-account name — keep host name (should match uid form).
			name = physName
		}
		if isLegacyWorkbuddyAuthName(physName) || isLegacyWorkbuddyAuthName(filepath.Base(path)) {
			if sa != nil && strings.TrimSpace(sa.Account.UID) != "" {
				// Migrate: write canonical, delete legacy path after save.
				legacyPath = path
				if isLegacyWorkbuddyAuthName(filepath.Base(path)) {
					// path stays legacy until we write canonical beside it
				}
				// After persist to name, remove legacyPath if different.
			}
		}
	}
	return name, path, legacyPath
}

// hostAuthPersist saves via host API only. Dual-writing the physical path after
// a successful host.auth.save is redundant (host already WriteFile) and can
// re-fire the watcher → extra re-parse / transient dual registration risk.
func hostAuthPersist(name, path string, raw []byte) error {
	_ = path // reserved for callers that still pass physical path for migrate logic
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	return hostAuthSaveJSON(name, raw)
}

// hostAuthPersistMigrate is like hostAuthPersist but also removes a legacy path
// when the canonical name differs (workbuddy.json → workbuddy-<uid>.json).
func hostAuthPersistMigrate(name, path, legacyPath string, raw []byte) error {
	if err := hostAuthPersist(name, path, raw); err != nil {
		return err
	}
	// If path was legacy and name is canonical, also write canonical path next to it.
	if legacyPath != "" && !strings.EqualFold(filepath.Base(legacyPath), name) {
		// host.auth.save already wrote name under auth dir; drop legacy file.
		if isSafeWorkbuddyAuthPath(legacyPath) && isLegacyWorkbuddyAuthName(filepath.Base(legacyPath)) {
			_ = os.Remove(legacyPath)
		}
	}
	// If path points at legacy but name is uid form, do not dual-write path (would keep legacy alive).
	return nil
}

// buildAuthFileJSON produces host-save payload: nested storage + top-level metadata.
// extra merges additional top-level keys (optional).
func buildAuthFileJSON(sa *storedAuth, disabled bool, note string, extra map[string]any) ([]byte, error) {
	if sa == nil {
		return nil, fmt.Errorf("nil storedAuth")
	}
	storage, err := json.Marshal(sa)
	if err != nil {
		return nil, err
	}
	var nested map[string]any
	if err := json.Unmarshal(storage, &nested); err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":     providerName,
		"provider": providerName,
		"logo":     pluginLogoURL,
		"disabled": disabled,
		"note":     note,
		"auth":     nested["auth"],
		"account":  nested["account"],
	}
	for k, v := range extra {
		out[k] = v
	}
	return json.Marshal(out)
}

// parseDisabledFromAuthJSON reads top-level disabled from physical auth JSON.
func parseDisabledFromAuthJSON(raw []byte) bool {
	var m struct {
		Disabled bool `json:"disabled"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Disabled
}

// isSafeWorkbuddyAuthPath rejects non-workbuddy filenames and empty paths.
func isSafeWorkbuddyAuthPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	if !strings.HasPrefix(lower, "workbuddy-") && lower != "workbuddy.json" {
		return false
	}
	if !strings.HasSuffix(lower, ".json") {
		return false
	}
	// Path traversal / absolute weirdness: base must equal cleaned base.
	if base != filepath.Base(filepath.Clean(path)) {
		return false
	}
	return true
}

// deleteAuthFileAt removes a workbuddy auth file. Missing file is success.
func deleteAuthFileAt(path string) error {
	if !isSafeWorkbuddyAuthPath(path) {
		return fmt.Errorf("refusing to delete unsafe path: %s", path)
	}
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// hostAuthGetFull returns physical JSON, path, and name for an auth index.
type hostAuthPhysical struct {
	AuthIndex string
	Name      string
	Path      string
	JSON      []byte
	Disabled  bool
}

func hostAuthGetPhysical(authIndex string) (*hostAuthPhysical, error) {
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
	return &hostAuthPhysical{
		AuthIndex: resp.AuthIndex,
		Name:      resp.Name,
		Path:      resp.Path,
		JSON:      resp.JSON,
		Disabled:  parseDisabledFromAuthJSON(resp.JSON),
	}, nil
}

// hostAuthSaveJSON persists credential JSON via host.auth.save.
func hostAuthSaveJSON(name string, raw []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty auth file name")
	}
	saveReq := pluginapi.HostAuthSaveRequest{
		Name: name,
		JSON: raw,
	}
	saveBody, _ := json.Marshal(saveReq)
	rawResp, err := hostCall(pluginabi.MethodHostAuthSave, saveBody)
	if err != nil {
		return fmt.Errorf("host.auth.save: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(rawResp, &env); err != nil || !env.OK {
		msg := "host.auth.save failed"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// writeAuthFileIfSafe best-effort overwrites a workbuddy auth path so the host
// file watcher re-synthesizes Auth.Disabled from metadata.disabled.
// CPA host.auth.save upsert may leave in-memory Disabled lagging until resync.
func writeAuthFileIfSafe(path string, raw []byte) error {
	path = strings.TrimSpace(path)
	if !isSafeWorkbuddyAuthPath(path) {
		return nil
	}
	if len(raw) == 0 {
		return fmt.Errorf("empty auth payload")
	}
	return os.WriteFile(path, raw, 0o600)
}

// lifecycleStateUnchanged avoids redundant saves when note/disabled unchanged.
var (
	lifecycleState   sync.Map // auth_index -> lifecycleStateEntry
	lifecycleSaveTTL = 30 * time.Second
)

type lifecycleStateEntry struct {
	disabled bool
	note     string
	at       time.Time
}

func lifecycleStateUnchanged(authIndex string, disabled bool, note string) bool {
	v, ok := lifecycleState.Load(authIndex)
	if !ok {
		return false
	}
	e := v.(*lifecycleStateEntry)
	if e.disabled != disabled || e.note != note {
		return false
	}
	return time.Since(e.at) < lifecycleSaveTTL
}

func rememberLifecycleState(authIndex string, disabled bool, note string) {
	lifecycleState.Store(authIndex, &lifecycleStateEntry{disabled: disabled, note: note, at: time.Now()})
}

// pruneLifecycleState removes entries for auth indices that no longer exist
// or whose TTL has expired. Called from dashboard prune to prevent unbounded growth.
func pruneLifecycleState() {
	files, err := hostAuthList()
	if err != nil {
		return
	}
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[f.AuthIndex] = struct{}{}
	}
	lifecycleState.Range(func(key, value any) bool {
		idx, _ := key.(string)
		if _, ok := live[idx]; !ok {
			lifecycleState.Delete(key)
			return true
		}
		if e, ok := value.(*lifecycleStateEntry); ok && time.Since(e.at) > 10*time.Minute {
			lifecycleState.Delete(key)
		}
		return true
	})
}

// disableAuth writes disabled:true for a CN (or fallback) account.
func disableAuth(authIndex string, sa *storedAuth, cr *creditsSummary, reason string) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	note := displayNote(sa, cr, true)
	if reason != "" && !strings.Contains(note, reason) {
		// keep note short; reason only if room
		if len(note)+len(reason) < 75 {
			note = note + " · " + reason
		}
	}
	if lifecycleStateUnchanged(authIndex, true, note) {
		return nil
	}
	// Prefer live physical file to preserve any extra fields if present.
	phys, err := hostAuthGetPhysical(authIndex)
	if err == nil && parseDisabledFromAuthJSON(phys.JSON) {
		// already disabled; still refresh note if needed
		if lifecycleStateUnchanged(authIndex, true, note) {
			return nil
		}
	}
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	if phys != nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
	}
	raw, err := buildAuthFileJSON(sa, true, note, nil)
	if err != nil {
		return err
	}
	if err := hostAuthPersistMigrate(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authIndex, true, note)
	accountCache.Delete(authIndex)
	return nil
}

// reenableAuth writes disabled:false when CN has credits again.
func reenableAuth(authIndex string, sa *storedAuth, cr *creditsSummary) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	if !shouldReenableCN(true, cr) {
		return nil
	}
	note := displayNote(sa, cr, false)
	if lifecycleStateUnchanged(authIndex, false, note) {
		return nil
	}
	phys, err := hostAuthGetPhysical(authIndex)
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	if err == nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
	}
	raw, err := buildAuthFileJSON(sa, false, note, nil)
	if err != nil {
		return err
	}
	if err := hostAuthPersistMigrate(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authIndex, false, note)
	accountCache.Delete(authIndex)
	return nil
}

// deleteAuth removes Global exhausted credentials from disk.
func deleteAuth(authIndex string, sa *storedAuth) error {
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()

	phys, err := hostAuthGetPhysical(authIndex)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(phys.Path)
	if path == "" {
		// Try to reconstruct path from peer workbuddy files' directory + canonical name.
		name := authFileNameFor(sa)
		if phys.Name != "" && !isLegacyWorkbuddyAuthName(phys.Name) {
			name = phys.Name
		} else if strings.TrimSpace(phys.Name) != "" && isLegacyWorkbuddyAuthName(phys.Name) {
			name = authFileNameFor(sa)
		}
		if dir := peerAuthDir(); dir != "" && name != "" {
			candidate := filepath.Join(dir, name)
			if isSafeWorkbuddyAuthPath(candidate) {
				path = candidate
			}
		}
	}
	if path == "" {
		// Last resort: disable instead of silent no-op (never invent a random path).
		note := displayNote(sa, nil, true) + " · 应删除但无 path"
		raw, berr := buildAuthFileJSON(sa, true, note, nil)
		if berr != nil {
			return fmt.Errorf("no path and build failed: %w", berr)
		}
		name := authFileNameFor(sa)
		if phys.Name != "" && !isLegacyWorkbuddyAuthName(phys.Name) {
			name = phys.Name
		}
		if err := hostAuthPersistMigrate(name, "", "", raw); err != nil {
			return err
		}
		rememberLifecycleState(authIndex, true, note)
		accountCache.Delete(authIndex)
		return nil
	}
	if err := deleteAuthFileAt(path); err != nil {
		return err
	}
	// Also remove legacy workbuddy.json if this UID was dual-named historically.
	if sa != nil && strings.TrimSpace(sa.Account.UID) != "" {
		if dir := filepath.Dir(path); dir != "" {
			legacy := filepath.Join(dir, authFileName)
			if isSafeWorkbuddyAuthPath(legacy) && isLegacyWorkbuddyAuthName(filepath.Base(legacy)) {
				_ = os.Remove(legacy)
			}
		}
	}
	lifecycleState.Delete(authIndex)
	accountCache.Delete(authIndex)
	return nil
}

// peerAuthDir returns the directory of any workbuddy auth file known to the host.
func peerAuthDir() string {
	files, err := hostAuthList()
	if err != nil {
		return ""
	}
	for _, f := range files {
		phys, err := hostAuthGetPhysical(f.AuthIndex)
		if err != nil || phys == nil {
			continue
		}
		p := strings.TrimSpace(phys.Path)
		if p == "" {
			continue
		}
		return filepath.Dir(p)
	}
	return ""
}

// applyExhaustedPolicy applies disable (CN) or delete (Global).
func applyExhaustedPolicy(authIndex string, sa *storedAuth, cr *creditsSummary, reason string) error {
	if !lifecycleEnabled() {
		return nil
	}
	action := lifecycleActionFor(accountRegion(sa), cr)
	switch action {
	case lifecycleDelete:
		return deleteAuth(authIndex, sa)
	case lifecycleDisable:
		return disableAuth(authIndex, sa, cr, reason)
	default:
		return nil
	}
}

// syncAuthNote writes note without changing disabled state.
func syncAuthNote(authIndex string, sa *storedAuth, cr *creditsSummary, disabled bool) error {
	if sa == nil {
		return nil
	}
	note := displayNote(sa, cr, disabled)
	if lifecycleStateUnchanged(authIndex, disabled, note) {
		return nil
	}
	mu := checkinLockFor(authIndex)
	mu.Lock()
	defer mu.Unlock()
	phys, err := hostAuthGetPhysical(authIndex)
	name := authFileNameFor(sa)
	path := ""
	legacyPath := ""
	if err == nil {
		name, path, legacyPath = resolveAuthFileTarget(sa, phys)
		// re-read disabled from disk as source of truth
		disabled = parseDisabledFromAuthJSON(phys.JSON)
		note = displayNote(sa, cr, disabled)
	}
	if lifecycleStateUnchanged(authIndex, disabled, note) {
		return nil
	}
	raw, err := buildAuthFileJSON(sa, disabled, note, nil)
	if err != nil {
		return err
	}
	if err := hostAuthPersistMigrate(name, path, legacyPath, raw); err != nil {
		return err
	}
	rememberLifecycleState(authIndex, disabled, note)
	return nil
}

// reconcileOneAccount refreshes credits and applies lifecycle for one auth.
// force ignores short-circuit only for credit fetch (uses force on cache via caller).
func reconcileOneAccount(authIndex string, force bool) (action lifecycleAction, err error) {
	if !lifecycleEnabled() {
		return lifecycleNone, nil
	}
	// Single host.auth.get (A-19): previous hostAuthGet + hostAuthGetPhysical
	// doubled RPC on every reconcile tick (21 accounts × 2).
	sa, phys, err := hostAuthGetBundle(authIndex)
	if err != nil {
		return lifecycleNone, err
	}
	disabled := false
	if phys != nil {
		disabled = phys.Disabled
	}

	// Credits: use force path via fetchUserResource always when force,
	// else try cache first.
	var cr *creditsSummary
	if !force {
		if v, ok := accountCache.Load(authIndex); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 && e.credits != nil && time.Since(e.fetched) < accountCacheTTL {
				cr = e.credits
			}
		}
	}
	if cr == nil {
		cr, err = fetchUserResource(sa)
		if err != nil {
			// unknown credits → no-op (safe default)
			return lifecycleNone, nil
		}
		// Merge into cache without wiping plan/checkin from dashboard fetch.
		entry := &accountCacheEntry{credits: cr, fetched: time.Now()}
		if v, ok := accountCache.Load(authIndex); ok {
			if prev, ok2 := v.(*accountCacheEntry); ok2 {
				entry.plan = prev.plan
				entry.checkin = prev.checkin
			}
		}
		accountCache.Store(authIndex, entry)
	}

	region := accountRegion(sa)
	if region == "cn" && disabled {
		if shouldReenableCN(true, cr) {
			if err := reenableAuth(authIndex, sa, cr); err != nil {
				return lifecycleReenable, err
			}
			return lifecycleReenable, nil
		}
		// still disabled: refresh note
		_ = syncAuthNote(authIndex, sa, cr, true)
		return lifecycleNone, nil
	}

	act := lifecycleActionFor(region, cr)
	switch act {
	case lifecycleDelete:
		return lifecycleDelete, deleteAuth(authIndex, sa)
	case lifecycleDisable:
		return lifecycleDisable, disableAuth(authIndex, sa, cr, "耗尽")
	default:
		// healthy: keep note fresh (throttled)
		_ = syncAuthNote(authIndex, sa, cr, false)
		return lifecycleNone, nil
	}
}

// reconcileAllAccounts walks workbuddy auths and applies lifecycle.
func reconcileAllAccounts(force bool) []map[string]any {
	if !lifecycleEnabled() {
		return nil
	}
	files, err := hostAuthList()
	if err != nil {
		return []map[string]any{{"error": err.Error()}}
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		act, err := reconcileOneAccount(f.AuthIndex, force)
		row := map[string]any{"auth_index": f.AuthIndex, "action": act.String()}
		if err != nil {
			row["error"] = err.Error()
		}
		if act != lifecycleNone || err != nil {
			out = append(out, row)
		}
	}
	return out
}

// reconcileAfterExecutorError triggers lifecycle when upstream reports hard credit failure.
// AuthID from the executor may be the credential ID (UID) rather than runtime auth_index;
// we resolve via host.auth.list when direct get fails.
func reconcileAfterExecutorError(authID string, status int, body string) {
	if !lifecycleEnabled() || strings.TrimSpace(authID) == "" {
		return
	}
	if isSoftRateLimit(status, body) && !isHardCreditError(status, body) {
		return
	}
	if !isHardCreditError(status, body) {
		return
	}
	go func() {
		idx := resolveAuthIndex(authID)
		if idx == "" {
			return
		}
		_, _ = reconcileOneAccount(idx, true)
	}()
}

// resolveAuthIndex maps executor AuthID (index, file id, or account UID) to host auth_index.
func resolveAuthIndex(authID string) string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ""
	}
	// Fast path: already an auth_index the host understands.
	if _, err := hostAuthGet(authID); err == nil {
		return authID
	}
	files, err := hostAuthList()
	if err != nil {
		return ""
	}
	for _, f := range files {
		if f.AuthIndex == authID || f.ID == authID || f.Name == authID {
			return f.AuthIndex
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			continue
		}
		if strings.TrimSpace(sa.Account.UID) == authID {
			return f.AuthIndex
		}
	}
	return ""
}

// reconcileByUID finds workbuddy auth by account UID and applies executor-error lifecycle.
func reconcileByUID(uid string, status int, body string) {
	uid = strings.TrimSpace(uid)
	if uid == "" || !lifecycleEnabled() {
		return
	}
	if !isHardCreditError(status, body) {
		return
	}
	idx := resolveAuthIndex(uid)
	if idx == "" {
		return
	}
	_, _ = reconcileOneAccount(idx, true)
}

// invalidateAccountCredits drops cached credits so the next panel/reconcile
// fetch hits upstream. Call after a successful chat completion — otherwise a
// 45s–5m cache makes "used" look frozen while the user is burning credits.
func invalidateAccountCredits(authID, authUID string) {
	if authID != "" {
		accountCache.Delete(authID)
	}
	if authUID == "" || authUID == authID {
		return
	}
	// Also drop any cache keyed by auth_index that maps to this UID.
	files, err := hostAuthList()
	if err != nil {
		return
	}
	for _, f := range files {
		if f.AuthIndex == authID || f.ID == authID || f.Name == authID {
			accountCache.Delete(f.AuthIndex)
			continue
		}
		sa, err := hostAuthGet(f.AuthIndex)
		if err != nil {
			continue
		}
		if strings.TrimSpace(sa.Account.UID) == authUID {
			accountCache.Delete(f.AuthIndex)
		}
	}
}

// enrichAuthMetadata builds Metadata map for AuthData (type/logo/note/disabled).
func enrichAuthMetadata(sa *storedAuth, cr *creditsSummary, disabled bool) map[string]any {
	note := displayNote(sa, cr, disabled)
	return map[string]any{
		"type":     providerName,
		"provider": providerName,
		"logo":     pluginLogoURL,
		"note":     note,
		"disabled": disabled,
	}
}
