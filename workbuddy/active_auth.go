// active_auth.go tracks the panel-selected WorkBuddy account used for routing.
//
// Region (CN vs Global) is taken from that account's stored domain field —
// no per-request JWT iss decode. Default: first available candidate. When the
// active account is exhausted/disabled/missing, randomly switch to another
// non-exhausted candidate and remember the choice.
package main

import (
	"math/rand"
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
// Preference: current selection if still viable → else random non-exhausted →
// else first candidate. Updates activeAuthID when it changes.
func pickActiveAuth(candidates []activeAuthCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	byID := make(map[string]activeAuthCandidate, len(candidates))
	var viable []activeAuthCandidate
	for _, c := range candidates {
		byID[c.ID] = c
		if c.Disabled || c.Exhausted {
			continue
		}
		viable = append(viable, c)
	}

	cur := getActiveAuthID()
	if cur != "" {
		if c, ok := byID[cur]; ok && !c.Disabled && !c.Exhausted {
			return cur
		}
	}

	// Active missing/disabled/exhausted → pick next.
	var next string
	if len(viable) > 0 {
		next = viable[rand.Intn(len(viable))].ID
	} else {
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
