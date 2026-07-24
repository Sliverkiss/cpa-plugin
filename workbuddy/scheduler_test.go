package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func parsePickResponse(t *testing.T, raw []byte) pluginapi.SchedulerPickResponse {
	t.Helper()
	var env struct {
		OK     bool                           `json:"ok"`
		Result pluginapi.SchedulerPickResponse `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatal("envelope not ok")
	}
	return env.Result
}

func resetActiveAuth(t *testing.T) {
	t.Helper()
	setActiveAuthID("")
	t.Cleanup(func() { setActiveAuthID("") })
}

func TestSchedulerPick_NonWorkbuddy_Defers(t *testing.T) {
	resetActiveAuth(t)
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: "other",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "ds-1", Provider: "dashscope"},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if resp.Handled {
		t.Fatal("non-workbuddy candidates should defer")
	}
}

func TestSchedulerPick_SingleCandidate_PicksIt(t *testing.T) {
	resetActiveAuth(t)
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-only", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-only" {
		t.Fatalf("want wb-only handled, got %+v", resp)
	}
	if getActiveAuthID() != "wb-only" {
		t.Fatalf("active auth should stick to wb-only, got %q", getActiveAuthID())
	}
}

func TestSchedulerPick_PrefersPanelSelection(t *testing.T) {
	resetActiveAuth(t)
	accountCache.Store("wb-a", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 10, TotalSize: 10}})
	accountCache.Store("wb-b", &accountCacheEntry{credits: &creditsSummary{TotalRemain: 500, TotalSize: 500}})
	defer func() {
		accountCache.Delete("wb-a")
		accountCache.Delete("wb-b")
	}()
	setActiveAuthID("wb-a")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-a", Provider: providerName},
			{ID: "wb-b", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-a" {
		t.Fatalf("want panel selection wb-a, got %+v", resp)
	}
}

func TestSchedulerPick_StaysOnExhaustedSelection(t *testing.T) {
	resetActiveAuth(t)
	// When selected is exhausted AND a non-exhausted candidate exists,
	// it should switch to the non-exhausted one and update activeAuthID.
	accountCache.Store("wb-exhausted", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 0, TotalUsed: 500, TotalSize: 500},
	})
	accountCache.Store("wb-ok", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 300, TotalUsed: 0, TotalSize: 300},
	})
	defer func() {
		accountCache.Delete("wb-exhausted")
		accountCache.Delete("wb-ok")
	}()
	setActiveAuthID("wb-exhausted")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-exhausted", Provider: providerName},
			{ID: "wb-ok", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-ok" {
		t.Fatalf("want switch to wb-ok, got %+v", resp)
	}
	if getActiveAuthID() != "wb-ok" {
		t.Fatalf("active should update to wb-ok, got %q", getActiveAuthID())
	}
}

func TestSchedulerPick_AllExhausted_KeepsCurrent(t *testing.T) {
	resetActiveAuth(t)
	// When ALL candidates are exhausted, keep current selection rather than
	// flip-flopping between exhausted accounts.
	accountCache.Store("wb-a", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 0, TotalUsed: 100, TotalSize: 100},
	})
	accountCache.Store("wb-b", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 0, TotalUsed: 200, TotalSize: 200},
	})
	defer func() {
		accountCache.Delete("wb-a")
		accountCache.Delete("wb-b")
	}()
	setActiveAuthID("wb-a")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-a", Provider: providerName},
			{ID: "wb-b", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-a" {
		t.Fatalf("want stay on wb-a (all exhausted), got %+v", resp)
	}
}

func TestSchedulerPick_SwitchesOnlyWhenSelectionGone(t *testing.T) {
	resetActiveAuth(t)
	accountCache.Store("wb-ok", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 300, TotalUsed: 0, TotalSize: 300},
	})
	defer accountCache.Delete("wb-ok")
	// Selected auth is NOT in candidates (host disabled it) → should switch.
	setActiveAuthID("wb-gone")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-ok", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-ok" {
		t.Fatalf("want switch to wb-ok, got %+v", resp)
	}
	if getActiveAuthID() != "wb-ok" {
		t.Fatalf("active should update to wb-ok, got %q", getActiveAuthID())
	}
}

func TestSchedulerPick_SkipsDisabledCandidates(t *testing.T) {
	resetActiveAuth(t)
	accountCache.Store("wb-live", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 50, TotalSize: 50},
	})
	defer accountCache.Delete("wb-live")
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-off", Provider: providerName, Status: "disabled"},
			{ID: "wb-live", Provider: providerName, Status: "active"},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-live" {
		t.Fatalf("want wb-live, got %+v", resp)
	}
	// All disabled → defer
	raw2, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-off", Provider: providerName, Status: "disabled", Metadata: map[string]any{"disabled": true}},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp2 := parsePickResponse(t, raw2)
	if resp2.Handled {
		t.Fatalf("all disabled should defer, got %+v", resp2)
	}
}

func TestCandidateDisabled(t *testing.T) {
	if !candidateDisabled(pluginapi.SchedulerAuthCandidate{Status: "disabled"}) {
		t.Fatal("status disabled")
	}
	if !candidateDisabled(pluginapi.SchedulerAuthCandidate{Metadata: map[string]any{"disabled": true}}) {
		t.Fatal("meta disabled")
	}
	if candidateDisabled(pluginapi.SchedulerAuthCandidate{Status: "active"}) {
		t.Fatal("active should not be disabled")
	}
}

func TestEnsureDefaultActiveAuth(t *testing.T) {
	resetActiveAuth(t)
	id := ensureDefaultActiveAuth([]wbAccount{
		{AuthIndex: "a1", Disabled: true},
		{AuthIndex: "a2", Exhausted: false},
		{AuthIndex: "a3"},
	})
	if id != "a2" {
		t.Fatalf("want first ready a2, got %q", id)
	}
	if getActiveAuthID() != "a2" {
		t.Fatalf("stuck active %q", getActiveAuthID())
	}
	// Already set + still live → keep
	id2 := ensureDefaultActiveAuth([]wbAccount{
		{AuthIndex: "a2"},
		{AuthIndex: "a3"},
	})
	if id2 != "a2" {
		t.Fatalf("should keep a2, got %q", id2)
	}
}
