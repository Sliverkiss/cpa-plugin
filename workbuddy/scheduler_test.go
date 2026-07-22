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
		OK     bool                       `json:"ok"`
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

func TestSchedulerPick_OffMode_Defers(t *testing.T) {
	restore := setSchedulerMode(schedulerModeOff)
	defer restore()
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-1", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if resp.Handled {
		t.Fatal("off mode should defer")
	}
}

func TestSchedulerPick_NonWorkbuddy_Defers(t *testing.T) {
	restore := setSchedulerMode(schedulerModeCredits)
	defer restore()
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
	restore := setSchedulerMode(schedulerModeCredits)
	defer restore()
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
}

func TestSchedulerPick_CreditsMode_PicksHigherRemain(t *testing.T) {
	// Seed accountCache with two entries: wb-low (remain 100) and wb-high (remain 500).
	accountCache.Store("wb-low", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 100},
	})
	accountCache.Store("wb-high", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 500},
	})
	defer func() {
		accountCache.Delete("wb-low")
		accountCache.Delete("wb-high")
	}()

	restore := setSchedulerMode(schedulerModeCredits)
	defer restore()
	raw, err := handleSchedulerPick(mustMarshal(t, pluginapi.SchedulerPickRequest{
		Provider: providerName,
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "wb-low", Provider: providerName},
			{ID: "wb-high", Provider: providerName},
		},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := parsePickResponse(t, raw)
	if !resp.Handled || resp.AuthID != "wb-high" {
		t.Fatalf("want wb-high (remain 500), got %+v", resp)
	}
}

func TestSchedulerPick_CreditsMode_NoCache_StillPicks(t *testing.T) {
	// No cache entries — remain defaults to -1 for both, sort is stable,
	// should still pick the first candidate (not crash, not defer).
	restore := setSchedulerMode(schedulerModeCredits)
	defer restore()
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
	if !resp.Handled {
		t.Fatal("should still handle when no cache (stable sort picks first)")
	}
}

func TestSchedulerPick_CreditsMode_SkipsExhausted(t *testing.T) {
	// wb-exhausted has remain=0, wb-ok has remain=300 → should pick wb-ok.
	accountCache.Store("wb-exhausted", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 0},
	})
	accountCache.Store("wb-ok", &accountCacheEntry{
		credits: &creditsSummary{TotalRemain: 300},
	})
	defer func() {
		accountCache.Delete("wb-exhausted")
		accountCache.Delete("wb-ok")
	}()

	restore := setSchedulerMode(schedulerModeCredits)
	defer restore()
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
		t.Fatalf("want wb-ok (remain 300 > 0), got %+v", resp)
	}
}
