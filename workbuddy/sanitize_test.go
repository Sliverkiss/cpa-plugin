package main

import "testing"

func TestSanitizeBlockedTemplates_ClaudeCode(t *testing.T) {
	in := "You are Claude Code, Anthropic's official CLI for Claude."
	out := sanitizeBlockedTemplates(in)
	if out == in {
		t.Fatal("should replace blocked template")
	}
	want := "You are Claude Code, Anthropic's official CLI tool for Claude."
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestSanitizeBlockedTemplates_MainBranch(t *testing.T) {
	in := "Main branch (you will usually use this for PRs)"
	out := sanitizeBlockedTemplates(in)
	if out == in {
		t.Fatal("should replace Main branch")
	}
}

func TestSanitizeBlockedTemplates_NoMatch(t *testing.T) {
	in := "Hello world"
	out := sanitizeBlockedTemplates(in)
	if out != in {
		t.Fatal("should pass through unchanged")
	}
}

func TestForceMaxThinking_Hy3Model(t *testing.T) {
	obj := map[string]any{"model": "hy3-std"}
	changed := forceMaxThinking(obj)
	if !changed {
		t.Fatal("should change hy3 model")
	}
	if obj["reasoning_effort"] != "high" {
		t.Fatal("should set high")
	}
}

func TestForceMaxThinking_NonHy3Model(t *testing.T) {
	obj := map[string]any{"model": "glm-5.2"}
	changed := forceMaxThinking(obj)
	if changed {
		t.Fatal("should not change non-hy3 model")
	}
}

func TestForceMaxThinking_AlreadyHigh(t *testing.T) {
	obj := map[string]any{"model": "hy3-std", "reasoning_effort": "high"}
	changed := forceMaxThinking(obj)
	if changed {
		t.Fatal("should not change when already high")
	}
}


func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Fatal("short string should be unchanged")
	}
	if truncate("hello world", 5) != "hello" {
		t.Fatal("should truncate to 5 chars")
	}
	if truncate("", 5) != "" {
		t.Fatal("empty string")
	}
}
