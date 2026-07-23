package main

import "testing"

func TestSummarizeCredits(t *testing.T) {
	accounts := []wbAccount{
		{
			Region: "cn",
			Credits: &creditsSummary{TotalRemain: 100, TotalUsed: 50, Packages: []packageSummary{{Name: "a"}}},
		},
		{
			Region: "global",
			Credits: &creditsSummary{TotalRemain: 20, TotalUsed: 230, Packages: []packageSummary{{Name: "b"}}},
		},
		{
			Region:   "cn",
			Disabled: true,
			// unknown credits — ignored in totals
		},
		{
			Region:    "cn",
			Exhausted: true,
			Credits:   &creditsSummary{TotalRemain: 0, TotalUsed: 10, Packages: []packageSummary{{Name: "c"}}},
		},
	}
	sum := summarizeCredits(accounts)
	if sum["total_remain"].(int64) != 120 {
		t.Fatalf("remain=%v want 120", sum["total_remain"])
	}
	if sum["total_used"].(int64) != 290 {
		t.Fatalf("used=%v want 290", sum["total_used"])
	}
	if sum["cn_remain"].(int64) != 100 {
		t.Fatalf("cn_remain=%v", sum["cn_remain"])
	}
	if sum["global_used"].(int64) != 230 {
		t.Fatalf("global_used=%v", sum["global_used"])
	}
	if sum["known_count"].(int) != 3 {
		t.Fatalf("known=%v", sum["known_count"])
	}
	if sum["disabled_count"].(int) != 1 {
		t.Fatalf("disabled=%v", sum["disabled_count"])
	}
	if sum["exhausted_count"].(int) != 1 {
		t.Fatalf("exhausted=%v", sum["exhausted_count"])
	}
}
