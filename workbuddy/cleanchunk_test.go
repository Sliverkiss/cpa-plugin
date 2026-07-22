package main

import (
	"strings"
	"testing"
)

func TestCleanChunkStripsLegacyShellInTerminalChunk(t *testing.T) {
	in := `{"choices":[{"index":0,"delta":{"role":"assistant","function_call":{"arguments":"","name":""}},"finish_reason":"tool_calls"}]}`
	got := cleanChunkJSON(in)
	if got == "" {
		t.Fatal("terminal chunk with finish_reason dropped")
	}
	if strings.Contains(got, "function_call") {
		t.Fatalf("legacy function_call shell not stripped: %s", got)
	}
	if !strings.Contains(got, `"finish_reason":"tool_calls"`) || !strings.Contains(got, `"role":"assistant"`) {
		t.Fatalf("meaningful fields lost: %s", got)
	}
}

func TestCleanChunkKeepsRealToolCallDeltas(t *testing.T) {
	// mid-stream fragment: real arguments flowing — must pass through untouched
	in := `{"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"\"","name":""},"index":0}]}}]}`
	got := cleanChunkJSON(in)
	if got != in {
		t.Fatalf("real tool_call fragment altered:\n in: %s\ngot: %s", in, got)
	}
	// non-empty function_call (legacy real call) must survive
	in2 := `{"choices":[{"index":0,"delta":{"function_call":{"arguments":"{}","name":"get_time"}}]}`
	if got := cleanChunkJSON(in2); got != in2 {
		t.Fatalf("real legacy function_call altered: %s", got)
	}
}

func TestIsEmptyValueRecursion(t *testing.T) {
	if !isEmptyValue(map[string]any{"name": "", "arguments": ""}) {
		t.Fatal("all-empty map should be empty")
	}
	if isEmptyValue(map[string]any{"name": "get_time", "arguments": ""}) {
		t.Fatal("map with non-empty value must not be empty")
	}
}

func TestCleanChunkStripsNoiseFields(t *testing.T) {
	in := `{"choices":[{"index":0,"delta":{"content":"hi","extra_fields":null,"refusal":"","reasoning_content":""}}]}`
	got := cleanChunkJSON(in)
	if got == "" {
		t.Fatal("chunk dropped")
	}
	for _, noise := range []string{"extra_fields", "refusal", "reasoning_content"} {
		if strings.Contains(got, noise) {
			t.Fatalf("noise field %s not stripped: %s", noise, got)
		}
	}
	if !strings.Contains(got, `"content":"hi"`) {
		t.Fatalf("content lost: %s", got)
	}
}
