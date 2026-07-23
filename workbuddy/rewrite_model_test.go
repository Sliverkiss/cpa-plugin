package main
import "testing"
func TestRewriteModelInBody(t *testing.T) {
	in := []byte(`{"model":"alias-name","messages":[{"role":"user","content":"hi"}]}`)
	out := rewriteModelInBody(in, "real-model-id")
	if !contains(string(out), "real-model-id") {
		t.Fatal("model not rewritten")
	}
	if contains(string(out), "alias-name") {
		t.Fatal("old model still present")
	}
}
func TestRewriteModelInBody_Empty(t *testing.T) {
	out := rewriteModelInBody([]byte(``), "m")
	if len(out) != 0 {
		t.Fatal("empty should pass through")
	}
}
func TestRewriteModelInBody_InvalidJSON(t *testing.T) {
	in := []byte(`not json`)
	out := rewriteModelInBody(in, "m")
	if string(out) != "not json" {
		t.Fatal("invalid JSON should pass through")
	}
}
