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
// account is no longer in the candidate list (disabled/deleted by host).
// This prevents silent drift between what the panel shows as selected and
// what the scheduler actually routes to.
//
// Exhausted accounts are NOT auto-switched — the panel shows the real
// selected card, and lifecycle (lifecycle.go) handles disable/delete when
// credits are truly gone. Only when the host removes the auth from
// candidates (disabled) do we fall back to the first available.
func pickActiveAuth(candidates []activeAuthCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	byID := make(map[string]activeAuthCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}

	cur := getActiveAuthID()
	// If current selection is still a live candidate, keep it — even if
	// exhausted. The panel shows this card as selected and lifecycle will
	// handle disable when appropriate.
	if cur != "" {
		if _, ok := byID[cur]; ok {
			return cur
		}
	}

	// Current selection is gone (disabled/deleted by host) — pick first
	// non-exhausted candidate, else first candidate. Update activeAuthID
	// so the panel can reflect the change on next dashboard load.
	var next string
	for _, c := range candidates {
		if !c.Exhausted {
			next = c.ID
			break
		}
	}
	if next == "" {
		next = candidates[0].ID
	}
	if next != "" && next != cur {
		setActiveAuthID(next)
	}
	return next
}

// ensureDefaultActiveAuth sets the first non-disabled account when none selected.
// Called from dashboard listing so panel shows a default without waiting for chat.
func ensureDefaultActiveAuth(accounts []wbAccount) string {
	cur := getActiveAuthID()
	live := make(map[string]wbAccount, len(accounts))
	for _, a := range accounts {
		live[a.AuthIndex] = a
	}
	if cur != "" {
		if a, ok := live[cur]; ok && !a.Disabled {
			return cur
		}
	}
	// Prefer first non-disabled non-exhausted, else first non-disabled, else first.
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
	next := firstReady
	if next == "" {
		next = firstOK
	}
	if next == "" {
		next = firstAny
	}
	if next != "" {
		setActiveAuthID(next)
	}
	return next
}
