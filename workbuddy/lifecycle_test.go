package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldActOnCredits(t *testing.T) {
	cases := []struct {
		name string
		cr   *creditsSummary
		want bool
	}{
		{"nil unknown", nil, false},
		{"empty unknown", &creditsSummary{}, false},
		{"remain>0", &creditsSummary{TotalRemain: 1}, false},
		{"exhausted used", &creditsSummary{TotalRemain: 0, TotalUsed: 10}, true},
		{"exhausted packages", &creditsSummary{TotalRemain: 0, Packages: []packageSummary{{Name: "p"}}}, true},
	}
	for _, tc := range cases {
		if got := shouldActOnCredits(tc.cr); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsHardCreditError(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{200, "", false},
		{500, "internal", false},
		{429, "too many requests", false}, // soft unless credit semantics
		{403, "insufficient credit", true},
		{400, "积分不足", true},
		{402, "payment required", true},
		{403, "额度不足", true},
		{400, "no credit left", true},
		{400, "余额不足", true},
		{403, "credit exhausted", true},
		{429, "rate limit exceeded", false},
		{400, "invalid request", false},
	}
	for _, tc := range cases {
		if got := isHardCreditError(tc.status, tc.body); got != tc.want {
			t.Fatalf("status=%d body=%q: got %v want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestIsSoftRateLimit(t *testing.T) {
	if !isSoftRateLimit(429, "too many requests") {
		t.Fatal("429 should be soft")
	}
	if isSoftRateLimit(403, "insufficient credit") {
		t.Fatal("hard credit must not be soft")
	}
	if isSoftRateLimit(500, "error") {
		t.Fatal("5xx not soft rate limit")
	}
}

func TestLifecycleActionFor(t *testing.T) {
	ex := &creditsSummary{TotalRemain: 0, TotalUsed: 5}
	ok := &creditsSummary{TotalRemain: 10, TotalUsed: 1}
	cases := []struct {
		name   string
		region string
		cr     *creditsSummary
		want   lifecycleAction
	}{
		{"cn exhausted", "cn", ex, lifecycleDisable},
		{"global exhausted", "global", ex, lifecycleDelete},
		{"cn ok", "cn", ok, lifecycleNone},
		{"global ok", "global", ok, lifecycleNone},
		{"unknown", "cn", nil, lifecycleNone},
	}
	for _, tc := range cases {
		if got := lifecycleActionFor(tc.region, tc.cr); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestShouldReenableCN(t *testing.T) {
	if shouldReenableCN(false, &creditsSummary{TotalRemain: 10}) {
		t.Fatal("enabled account should not reenable")
	}
	if shouldReenableCN(true, nil) {
		t.Fatal("unknown credits must not reenable")
	}
	if shouldReenableCN(true, &creditsSummary{TotalRemain: 0, TotalUsed: 5}) {
		t.Fatal("exhausted must not reenable")
	}
	if !shouldReenableCN(true, &creditsSummary{TotalRemain: 3}) {
		t.Fatal("disabled + remain should reenable")
	}
}

func TestDisplayNote(t *testing.T) {
	cn := &storedAuth{Auth: storedTokens{Domain: "www.codebuddy.cn"}}
	gl := &storedAuth{Auth: storedTokens{Domain: "www.workbuddy.ai"}}
	note := displayNote(cn, &creditsSummary{TotalRemain: 12, TotalUsed: 8}, false)
	if !strings.Contains(note, "CN") || !strings.Contains(note, "12") {
		t.Fatalf("cn note = %q", note)
	}
	note = displayNote(gl, &creditsSummary{TotalRemain: 0, TotalUsed: 250}, false)
	if !strings.Contains(note, "Global") {
		t.Fatalf("global note = %q", note)
	}
	note = displayNote(cn, &creditsSummary{TotalRemain: 0, TotalUsed: 5}, true)
	if !strings.Contains(note, "禁用") && !strings.Contains(strings.ToLower(note), "disabled") {
		t.Fatalf("disabled note = %q", note)
	}
}

func TestBuildAuthFileJSON_ContainsDisabledAndNote(t *testing.T) {
	sa := &storedAuth{
		Auth:    storedTokens{AccessToken: "at", RefreshToken: "rt", Domain: "www.codebuddy.cn"},
		Account: storedAccount{UID: "u1", Nickname: "nick"},
	}
	raw, err := buildAuthFileJSON(sa, true, "CN · test", nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != providerName {
		t.Fatalf("type=%v", m["type"])
	}
	if m["disabled"] != true {
		t.Fatalf("disabled=%v", m["disabled"])
	}
	if m["note"] != "CN · test" {
		t.Fatalf("note=%v", m["note"])
	}
	if m["logo"] == nil || m["logo"] == "" {
		t.Fatal("logo missing")
	}
	auth, _ := m["auth"].(map[string]any)
	if auth == nil || auth["accessToken"] != "at" {
		t.Fatalf("auth tokens lost: %v", m["auth"])
	}
}

func TestSafeWorkbuddyAuthPath(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "workbuddy-abc.json")
	if err := os.WriteFile(ok, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !isSafeWorkbuddyAuthPath(ok) {
		t.Fatalf("expected safe: %s", ok)
	}
	if isSafeWorkbuddyAuthPath(filepath.Join(dir, "other.json")) {
		t.Fatal("other.json must be rejected")
	}
	if isSafeWorkbuddyAuthPath("") {
		t.Fatal("empty rejected")
	}
	if isSafeWorkbuddyAuthPath("/etc/passwd") {
		t.Fatal("passwd rejected")
	}
}

func TestDeleteAuthFile_Idempotent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "workbuddy-x.json")
	if err := os.WriteFile(p, []byte(`{"type":"workbuddy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deleteAuthFileAt(p); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file should be gone")
	}
	if err := deleteAuthFileAt(p); err != nil {
		t.Fatalf("second delete should be idempotent: %v", err)
	}
}

func TestAuthFileNameFor(t *testing.T) {
	sa := &storedAuth{Account: storedAccount{UID: "uid-1"}}
	if got := authFileNameFor(sa); got != "workbuddy-uid-1.json" {
		t.Fatalf("got %q", got)
	}
	if got := authFileNameFor(nil); got != authFileName {
		t.Fatalf("nil got %q", got)
	}
}

func TestParseDisabledFromAuthJSON(t *testing.T) {
	if parseDisabledFromAuthJSON([]byte(`{"disabled":true}`)) != true {
		t.Fatal("want true")
	}
	if parseDisabledFromAuthJSON([]byte(`{"disabled":false}`)) != false {
		t.Fatal("want false")
	}
	if parseDisabledFromAuthJSON([]byte(`{"auth":{}}`)) != false {
		t.Fatal("missing defaults false")
	}
}

func TestLabelForAuth(t *testing.T) {
	sa := &storedAuth{
		Auth:    storedTokens{Domain: "www.workbuddy.ai"},
		Account: storedAccount{Nickname: "Bob"},
	}
	got := labelForAuth(sa)
	if !strings.Contains(got, "Bob") || !strings.Contains(got, "Global") {
		t.Fatalf("label=%q", got)
	}
}
