// scheduler.go implements the CPA scheduler.pick capability for workbuddy.
//
// When scheduler_mode is "off" (default), the plugin defers to the built-in
// scheduler so existing fill-first/round-robin behaviour is unchanged.
// When "credits", it picks the workbuddy candidate with the highest cached
// credit balance (TotalRemain). Non-workbuddy candidates are always deferred.
package main

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	schedulerModeOff     = "off"
	schedulerModeCredits = "credits"
)

var (
	schedulerMode   = schedulerModeOff
	schedulerModeMu sync.RWMutex
)

// setSchedulerMode is a test helper that returns a restore func.
func setSchedulerMode(mode string) func() {
	schedulerModeMu.Lock()
	old := schedulerMode
	schedulerMode = mode
	schedulerModeMu.Unlock()
	return func() {
		schedulerModeMu.Lock()
		schedulerMode = old
		schedulerModeMu.Unlock()
	}
}

func loadedSchedulerMode() string {
	schedulerModeMu.RLock()
	defer schedulerModeMu.RUnlock()
	return schedulerMode
}

// handleSchedulerPick selects a workbuddy auth candidate based on the
// configured scheduler_mode. Non-workbuddy candidates are always deferred
// (Handled: false) so the built-in scheduler handles them.
func handleSchedulerPick(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	mode := loadedSchedulerMode()
	if mode == schedulerModeOff {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	// Collect workbuddy candidates only; skip exhausted accounts when alternatives exist.
	var wbCandidates []pluginapi.SchedulerAuthCandidate
	for _, c := range req.Candidates {
		if c.Provider == providerName {
			wbCandidates = append(wbCandidates, c)
		}
	}
	if len(wbCandidates) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	// Prefer non-exhausted; if all exhausted still pick one (host may disable later).
	if mode == schedulerModeCredits {
		type scored struct {
			candidate pluginapi.SchedulerAuthCandidate
			remain    int64
			exhausted bool
		}
		items := make([]scored, 0, len(wbCandidates))
		for _, c := range wbCandidates {
			remain, exhausted := cachedCreditsScore(c.ID)
			items = append(items, scored{candidate: c, remain: remain, exhausted: exhausted})
		}
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].exhausted != items[j].exhausted {
				return !items[i].exhausted // non-exhausted first
			}
			return items[i].remain > items[j].remain
		})
		// If top is exhausted but a non-exhausted exists, sort already handled it.
		return okEnvelope(pluginapi.SchedulerPickResponse{
			AuthID:  items[0].candidate.ID,
			Handled: true,
		})
	}

	// Single candidate or unknown mode handling:
	if len(wbCandidates) == 1 {
		return okEnvelope(pluginapi.SchedulerPickResponse{
			AuthID:  wbCandidates[0].ID,
			Handled: true,
		})
	}

	// Unknown mode → defer.
	return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
}

// cachedCreditsRemain returns the cached TotalRemain for the given auth ID,
// or -1 if no cache entry exists. This is non-blocking: stale cache is fine
// for scheduler purposes — better to use last-known data than block the pick.
func cachedCreditsRemain(authID string) int64 {
	remain, _ := cachedCreditsScore(authID)
	return remain
}

// cachedCreditsScore returns (remain, exhausted) from accountCache.
// remain is -1 when unknown; exhausted uses isCreditsExhausted.
func cachedCreditsScore(authID string) (int64, bool) {
	v, ok := accountCache.Load(authID)
	if !ok {
		return -1, false
	}
	entry, ok := v.(*accountCacheEntry)
	if !ok || entry.credits == nil {
		return -1, false
	}
	return entry.credits.TotalRemain, isCreditsExhausted(entry.credits)
}
