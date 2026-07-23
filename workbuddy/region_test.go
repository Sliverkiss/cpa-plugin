package main

import "testing"

func TestIsGlobalDomain(t *testing.T) {
	cases := []struct {
		domain string
		want   bool
	}{
		{"www.workbuddy.ai", true},
		{"workbuddy.ai", true},
		{"www.codebuddy.cn", false},
		{"", false},
		{"WORKBUDDY.AI", true},
		{"  www.workbuddy.ai  ", true},
		// A-33: substring match was too loose
		{"evilworkbuddy.ai", false},
		{"workbuddy.ai.evil.com", false},
		{"notworkbuddy.ai", false},
	}
	for _, tc := range cases {
		if got := isGlobalDomain(tc.domain); got != tc.want {
			t.Errorf("isGlobalDomain(%q) = %v, want %v", tc.domain, got, tc.want)
		}
	}
}

func TestAccountRegion(t *testing.T) {
	cases := []struct {
		name   string
		sa     *storedAuth
		region string
	}{
		{"nil", nil, "cn"},
		{"empty domain", &storedAuth{}, "cn"},
		{"CN domain", &storedAuth{Auth: storedTokens{Domain: "www.codebuddy.cn"}}, "cn"},
		{"Global domain", &storedAuth{Auth: storedTokens{Domain: "www.workbuddy.ai"}}, "global"},
	}
	for _, tc := range cases {
		if got := accountRegion(tc.sa); got != tc.region {
			t.Errorf("%s: accountRegion() = %q, want %q", tc.name, got, tc.region)
		}
	}
}

func TestBillingBaseFor(t *testing.T) {
	cases := []struct {
		name string
		sa   *storedAuth
		want string
	}{
		{"nil", nil, billingBase},
		{"empty domain", &storedAuth{}, billingBase},
		{"CN domain", &storedAuth{Auth: storedTokens{Domain: "www.codebuddy.cn"}}, billingBase},
		{"Global domain", &storedAuth{Auth: storedTokens{Domain: "www.workbuddy.ai"}}, billingBaseGlobal},
	}
	for _, tc := range cases {
		if got := billingBaseFor(tc.sa); got != tc.want {
			t.Errorf("%s: billingBaseFor() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOriginRefererFor(t *testing.T) {
	cases := []struct {
		name string
		sa   *storedAuth
		want string
	}{
		{"nil", nil, originReferer},
		{"empty domain", &storedAuth{}, originReferer},
		{"CN domain", &storedAuth{Auth: storedTokens{Domain: "www.codebuddy.cn"}}, originReferer},
		{"Global domain", &storedAuth{Auth: storedTokens{Domain: "www.workbuddy.ai"}}, "https://www.workbuddy.ai"},
	}
	for _, tc := range cases {
		if got := originRefererFor(tc.sa); got != tc.want {
			t.Errorf("%s: originRefererFor() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
