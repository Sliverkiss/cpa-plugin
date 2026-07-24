// active_auth.go tracks the panel-selected WorkBuddy account used for routing.
//
// Region (CN vs Global) is taken from that account's stored domain field —
// no per-request JWT iss decode. Default: first available candidate. When the
// active account is exhausted/disabled/missing, randomly switch to another
// non-exhausted candidate and remember the choice.
package main

import (
	"strings"
	"sync"
)

var (
	activeAuthID string
	activeAuthMu sync.RWMutex
)

func getActiveAuthID() string {
	activeAuthMu.RLock()
	defer activeAuthMu.RUnlock()
	return strings.TrimSpace(activeAuthID)
}

func setActiveAuthID(id string) {
	id = strings.TrimSpace(id)
	activeAuthMu.Lock()
	activeAuthID = id
	activeAuthMu.Unlock()
}

// clearActiveAuthIfMatch clears the selection when the given auth is removed.
func clearActiveAuthIfMatch(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	activeAuthMu.Lock()
	if activeAuthID == id {
		activeAuthID = ""
	}
	activeAuthMu.Unlock()
}

// activeAuthCandidate is a thin view used by pickActiveAuth.
type activeAuthCandidate struct {
	ID        string
	Disabled  bool
	Exhausted bool
}

// pickActiveAuth chooses which workbuddy auth to use from host candidates.
// The panel selection is sticky: it stays on the current account unless that
// account is no longer in the candidate list (disabled/deleted by host) or
// is marked exhausted in cache. When switching, it picks the first
// non-exhausted candidate and updates activeAuthID so the panel reflects
// the change on next dashboard load.
func pickActiveAuth(candidates []activeAuthCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	byID := make(map[string]activeAuthCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}

	cur := getActiveAuthID()
	// Keep current selection if it's still a live candidate AND not exhausted.
	if cur != "" {
		if c, ok := byID[cur]; ok && !c.Exhausted {
			return cur
		}
	}

	// Selection is gone or exhausted — pick next non-exhausted, else first.
	var next string
	for _, c := range candidates {
		if !c.Exhausted {
			next = c.ID
			break
		}
	}
	if next == "" {
		// All exhausted — keep current if still alive, else first candidate.
		if cur != "" {
			if _, ok := byID[cur]; ok {
				return cur
			}
		}
		next = candidates[0].ID
	}
	if next != "" && next != cur {
		setActiveAuthID(next)
	}
	return next
}

// ensureDefaultActiveAuth sets the panel-selected account.
// Called from buildDashboardEx on every /accounts and /refresh request.
//
// Rules (single source of truth, same as pickActiveAuth):
//  1. If current selection is live AND not exhausted → keep it.
//  2. If current selection is exhausted → switch to first non-exhausted.
//  3. If current selection is gone (disabled/deleted) → switch to first available.
//  4. If all exhausted → keep current if alive, else first.
//
// This ensures the panel's selected card always matches what scheduler.pick
// actually routes to. No silent drift.
func ensureDefaultActiveAuth(accounts []wbAccount) string {
	cur := getActiveAuthID()
	live := make(map[string]wbAccount, len(accounts))
	for _, a := range accounts {
		live[a.AuthIndex] = a
	}

	// Rule 1: current selection is live AND not exhausted → keep.
	if cur != "" {
		if a, ok := live[cur]; ok && !a.Disabled && !a.Exhausted {
			return cur
		}
	}

	// Rule 2 & 3: selection is exhausted or gone → find next.
	var firstAny, firstOK, firstReady string
	for _, a := range accounts {
		if firstAny == "" {
			firstAny = a.AuthIndex
		}
		if a.Disabled {
			continue
		}
		if firstOK == "" {
			firstOK = a.AuthIndex
		}
		if !a.Exhausted && firstReady == "" {
			firstReady = a.AuthIndex
		}
	}

	// Prefer first non-exhausted non-disabled.
	next := firstReady
	if next == "" {
		// Rule 4: all exhausted — keep current if still alive (not disabled).
		if cur != "" {
			if a, ok := live[cur]; ok && !a.Disabled {
				return cur
			}
		}
		next = firstOK
	}
	if next == "" {
		next = firstAny
	}
	if next != "" && next != cur {
		setActiveAuthID(next)
	}
	return next
}
