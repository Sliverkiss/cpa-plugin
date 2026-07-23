package main

import "testing"

func TestPackageRemainUsed_CyclePreferred(t *testing.T) {
	// Free monthly pack: lifetime CapacityUsed=0 but cycle is exhausted.
	a := resourcePackage{
		PackageName:         "CodeBuddy个人体验版",
		CapacityRemain:      500,
		CapacityUsed:        0,
		CapacitySize:        500,
		CycleCapacityRemain: 0,
		CycleCapacitySize:   500,
		// CycleCapacityUsed omitted (zero-value) — must still report used=500.
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 0 || used != 500 || size != 500 {
		t.Fatalf("cycle-exhausted: remain=%d used=%d size=%d, want 0/500/500", remain, used, size)
	}
}

func TestPackageRemainUsed_CyclePartial(t *testing.T) {
	a := resourcePackage{
		CapacityRemain:      100,
		CapacityUsed:        0,
		CapacitySize:        100,
		CycleCapacityRemain: 99,
		CycleCapacitySize:   100,
		CycleCapacityUsed:   1,
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 99 || used != 1 || size != 100 {
		t.Fatalf("cycle-partial: remain=%d used=%d size=%d, want 99/1/100", remain, used, size)
	}
}

func TestPackageRemainUsed_CycleUsedOmitted(t *testing.T) {
	// CycleCapacityUsed missing; derive from size-remain.
	a := resourcePackage{
		CapacityRemain:      500,
		CapacityUsed:        0,
		CapacitySize:        500,
		CycleCapacityRemain: 499,
		CycleCapacitySize:   500,
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 499 || used != 1 || size != 500 {
		t.Fatalf("cycle-used-omitted: remain=%d used=%d size=%d, want 499/1/500", remain, used, size)
	}
}

func TestPackageRemainUsed_FallbackLifetime(t *testing.T) {
	// No cycle fields → use lifetime Capacity*.
	a := resourcePackage{
		CapacityRemain: 80,
		CapacityUsed:   20,
		CapacitySize:   100,
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 80 || used != 20 || size != 100 {
		t.Fatalf("lifetime: remain=%d used=%d size=%d, want 80/20/100", remain, used, size)
	}
}

func TestPackageRemainUsed_FallbackComputeUsed(t *testing.T) {
	// Lifetime Used=0 but Size>Remain → derive used.
	a := resourcePackage{
		CapacityRemain: 80,
		CapacityUsed:   0,
		CapacitySize:   100,
	}
	remain, used, size := packageRemainUsed(a)
	if remain != 80 || used != 20 || size != 100 {
		t.Fatalf("lifetime-derived: remain=%d used=%d size=%d, want 80/20/100", remain, used, size)
	}
}

func TestPackageRemainUsed_AggregateMultiPack(t *testing.T) {
	// Live shape: 体验版 exhausted + check-in packs (size grows with grants).
	// Daily check-in adds capacity; used is cycle spend, not inverse of remain.
	packs := []resourcePackage{
		{PackageName: "体验版", CapacityRemain: 500, CapacityUsed: 0, CapacitySize: 500, CycleCapacityRemain: 0, CycleCapacitySize: 500, CycleCapacityUsed: 500},
		{PackageName: "裂变包A", CapacityRemain: 99, CapacityUsed: 0, CapacitySize: 100, CycleCapacityRemain: 99, CycleCapacitySize: 100},
		{PackageName: "裂变包B", CapacityRemain: 100, CapacityUsed: 0, CapacitySize: 100, CycleCapacityRemain: 100, CycleCapacitySize: 100},
		{PackageName: "裂变包C", CapacityRemain: 100, CapacityUsed: 0, CapacitySize: 100, CycleCapacityRemain: 100, CycleCapacitySize: 100},
	}
	var totalRemain, totalUsed, totalSize int64
	for _, p := range packs {
		r, u, s := packageRemainUsed(p)
		totalRemain += r
		totalUsed += u
		totalSize += s
	}
	// remain 0+99+100+100=299; used 500+1+0+0=501; size 800
	if totalRemain != 299 || totalUsed != 501 || totalSize != 800 {
		t.Fatalf("multi-pack aggregate: remain=%d used=%d size=%d, want 299/501/800", totalRemain, totalUsed, totalSize)
	}
	// Spending 5 credits on 裂变包B: remain drops, used rises, size stable.
	packs[2].CycleCapacityRemain = 95
	totalRemain, totalUsed, totalSize = 0, 0, 0
	for _, p := range packs {
		r, u, s := packageRemainUsed(p)
		totalRemain += r
		totalUsed += u
		totalSize += s
	}
	if totalRemain != 294 || totalUsed != 506 || totalSize != 800 {
		t.Fatalf("after spend: remain=%d used=%d size=%d, want 294/506/800", totalRemain, totalUsed, totalSize)
	}
}

func TestIsCreditsExhausted(t *testing.T) {
	cases := []struct {
		name string
		cr   *creditsSummary
		want bool
	}{
		{"nil", nil, false},
		{"remain>0", &creditsSummary{TotalRemain: 10}, false},
		{"remain0 used>0", &creditsSummary{TotalRemain: 0, TotalUsed: 5}, true},
		{"remain0 size>0", &creditsSummary{TotalRemain: 0, TotalSize: 100}, true},
		{"remain0 packages", &creditsSummary{TotalRemain: 0, Packages: []packageSummary{{Name: "x"}}}, true},
		{"remain0 no data", &creditsSummary{TotalRemain: 0, TotalUsed: 0}, false},
	}
	for _, tc := range cases {
		if got := isCreditsExhausted(tc.cr); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
