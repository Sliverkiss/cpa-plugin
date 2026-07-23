package main
import "testing"
func TestMergeToolCallDelta_FirstDelta(t *testing.T) {
	merged := map[string]any{}
	delta := map[string]any{"id":"call_1","type":"function","function":map[string]any{"name":"get_time","arguments":"{}"}}
	mergeToolCallDelta(merged, delta)
	if merged["id"] != "call_1" { t.Fatal("id")
	}
	if merged["type"] != "function" { t.Fatal("type") }
	mfn := merged["function"].(map[string]any)
	if mfn["name"] != "get_time" { t.Fatal("name") }
	if mfn["arguments"] != "{}" { t.Fatal("arguments") }
}
func TestMergeToolCallDelta_AppendArguments(t *testing.T) {
	merged := map[string]any{"id":"call_1","function":map[string]any{"name":"get_time","arguments":"{\""}}
	delta := map[string]any{"function":map[string]any{"arguments":"location\":\"NYC\"}"}}
	mergeToolCallDelta(merged, delta)
	mfn := merged["function"].(map[string]any)
	args := mfn["arguments"].(string)
	want := `{"location":"NYC"}`;
	if args != want { t.Fatalf("args=%q want %q", args, want) }
}
func TestMergeToolCallDelta_NilFunction(t *testing.T) {
	merged := map[string]any{}
	delta := map[string]any{"id":"call_1"}
	mergeToolCallDelta(merged, delta)
	if merged["id"] != "call_1" { t.Fatal("id should be set") }
	if _, ok := merged["function"]; ok { t.Fatal("function should not be created") }
}
func TestMergeToolCallDelta_EmptyStringsIgnored(t *testing.T) {
	merged := map[string]any{"id":"call_1"}
	delta := map[string]any{"id":"","function":map[string]any{"name":"","arguments":""}}
	mergeToolCallDelta(merged, delta)
	if merged["id"] != "call_1" { t.Fatal("id should not be overwritten with empty") }
}
