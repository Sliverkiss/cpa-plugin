package main

import "testing"

func TestGlobalModelName(t *testing.T) {
	tests := map[string]string{
		"qmodel_preview":       "Qwen3.8-Max-Preview",
		"qwen3.8-max-preview":  "Qwen3.8-Max-Preview",
		"qoder/qmodel_preview": "Qwen3.8-Max-Preview",
		"qmodel_latest":        "Qwen3.7-Max",
	}
	for input, want := range tests {
		if got := globalModelName(input); got != want {
			t.Fatalf("globalModelName(%q) = %q, want %q", input, got, want)
		}
	}
}
