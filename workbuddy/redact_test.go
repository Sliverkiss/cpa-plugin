package main

import "testing"

func TestRedactSecrets(t *testing.T) {
	in := `upstream 401: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.sig rest`
	got := redactSecrets(in)
	if got == in {
		t.Fatal("expected redaction")
	}
	if containsJWT(got) {
		t.Fatalf("jwt still present: %s", got)
	}
	if !contains(got, "Bearer ***") && !contains(got, "***jwt***") {
		t.Fatalf("expected redaction markers: %s", got)
	}
}

func TestRedactSecrets_TokenKV(t *testing.T) {
	in := `error access_token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.aaaaaaaaaa.bbbbbbbbbb refresh_token=abcdef0123456789`
	got := redactSecrets(in)
	if contains(got, "abcdef0123456789") {
		t.Fatalf("refresh_token value not redacted: %s", got)
	}
	if !contains(got, "access_token=") || !contains(got, "refresh_token=") {
		t.Fatalf("key labels should remain: %s", got)
	}
}

func TestTruncateRedacted(t *testing.T) {
	// Long body with bearer must redact before truncation (A-37).
	tok := "Bearer " + "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0." + "sigsigsigsig"
	in := "upstream 401: " + tok + " " + repeat("x", 300)
	got := truncateRedacted(in, 80)
	if contains(got, "eyJhbGci") {
		t.Fatalf("token leaked after truncateRedacted: %s", got)
	}
	if len(got) > 80 {
		t.Fatalf("len=%d want <=80: %s", len(got), got)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func containsJWT(s string) bool {
	// crude: three base64url-ish segments starting with eyJ
	return indexOf(s, "eyJ") >= 0 && indexOf(s, ".eyJ") >= 0
}
