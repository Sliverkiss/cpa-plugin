package main
import "testing"
func TestUsageDetailFromMap(t *testing.T) {
	m := map[string]any{
		"total_tokens":      float64(100),
		"prompt_tokens":     float64(40),
		"completion_tokens": float64(60),
	}
	d := usageDetailFromMap(m)
	if d.TotalTokens != 100 { t.Fatalf("total=%d want 100", d.TotalTokens) }
	if d.InputTokens != 40 { t.Fatalf("input=%d want 40", d.InputTokens) }
	if d.OutputTokens != 60 { t.Fatalf("output=%d want 60", d.OutputTokens) }
}
func TestUsageDetailFromMap_Nil(t *testing.T) {
	d := usageDetailFromMap(nil)
	if d.TotalTokens != 0 { t.Fatal("nil should be zero") }
}
func TestUsageDetailFromMap_Partial(t *testing.T) {
	m := map[string]any{"total_tokens": float64(50)}
	d := usageDetailFromMap(m)
	if d.TotalTokens != 50 { t.Fatalf("total=%d want 50", d.TotalTokens) }
	// partial: only total set
}
func TestUsageDetailFromCompletion(t *testing.T) {
	payload := []byte(`{"usage":{"total_tokens":200,"prompt_tokens":80,"completion_tokens":120}}`)
	d := usageDetailFromCompletion(payload)
	if d.TotalTokens != 200 { t.Fatalf("total=%d want 200", d.TotalTokens) }
}
func TestUsageDetailFromCompletion_Invalid(t *testing.T) {
	d := usageDetailFromCompletion([]byte(`not json`))
	if d.TotalTokens != 0 { t.Fatal("invalid should be zero") }
}
func TestSseUsageCollector(t *testing.T) {
	c := &sseUsageCollector{}
	c.feed(`{"usage":{"total_tokens":42}}`)
	if c.last == nil || c.last["total_tokens"] == nil {
		t.Fatal("collector should capture usage")
	}
	c.feed(`{"choices":[]}`)  // no usage
	// last should still be the previous one
	if c.last["total_tokens"] == nil {
		t.Fatal("collector should retain last usage")
	}
}
