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
	remain, used := packageRemainUsed(a)
	if remain != 0 || used != 500 {
		t.Fatalf("cycle-exhausted: remain=%d used=%d, want 0/500", remain, used)
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
	remain, used := packageRemainUsed(a)
	if remain != 99 || used != 1 {
		t.Fatalf("cycle-partial: remain=%d used=%d, want 99/1", remain, used)
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
	remain, used := packageRemainUsed(a)
	if remain != 499 || used != 1 {
		t.Fatalf("cycle-used-omitted: remain=%d used=%d, want 499/1", remain, used)
	}
}

func TestPackageRemainUsed_FallbackLifetime(t *testing.T) {
	// No cycle fields → use lifetime Capacity*.
	a := resourcePackage{
		CapacityRemain: 80,
		CapacityUsed:   20,
		CapacitySize:   100,
	}
	remain, used := packageRemainUsed(a)
	if remain != 80 || used != 20 {
		t.Fatalf("lifetime: remain=%d used=%d, want 80/20", remain, used)
	}
}

func TestPackageRemainUsed_FallbackComputeUsed(t *testing.T) {
	// Lifetime Used=0 but Size>Remain → derive used.
	a := resourcePackage{
		CapacityRemain: 80,
		CapacityUsed:   0,
		CapacitySize:   100,
	}
	remain, used := packageRemainUsed(a)
	if remain != 80 || used != 20 {
		t.Fatalf("lifetime-derived: remain=%d used=%d, want 80/20", remain, used)
	}
}

func TestPackageRemainUsed_AggregateMultiPack(t *testing.T) {
	// Mirrors the live PxjRjE account shape: 体验版 exhausted + 2 裂变 packs.
	packs := []resourcePackage{
		{PackageName: "体验版", CapacityRemain: 500, CapacityUsed: 0, CapacitySize: 500, CycleCapacityRemain: 0, CycleCapacitySize: 500},
		{PackageName: "裂变包A", CapacityRemain: 99, CapacityUsed: 1, CapacitySize: 100, CycleCapacityRemain: 99, CycleCapacitySize: 100, CycleCapacityUsed: 1},
		{PackageName: "裂变包B", CapacityRemain: 100, CapacityUsed: 0, CapacitySize: 100, CycleCapacityRemain: 100, CycleCapacitySize: 100},
	}
	var totalRemain, totalUsed int64
	for _, p := range packs {
		r, u := packageRemainUsed(p)
		totalRemain += r
		totalUsed += u
	}
	// 0+99+100 = 199 remain; 500+1+0 = 501 used.
	if totalRemain != 199 || totalUsed != 501 {
		t.Fatalf("multi-pack aggregate: remain=%d used=%d, want 199/501", totalRemain, totalUsed)
	}
}
