package main

import "testing"

func TestPackageRemainUsed_NegativeRemainBranch2(t *testing.T) {
	// CycleCapacitySize=0 so branch1 skipped; Used>0 enters branch2 with Remain negative.
	a := resourcePackage{CycleCapacityRemain: -5, CycleCapacityUsed: 10}
	remain, used, size := packageRemainUsed(a)
	t.Logf("branch2 remain=%d used=%d size=%d", remain, used, size)
	if remain < 0 {
		t.Fatalf("remain should be clamped >=0, got %d", remain)
	}
	// size should still be non-negative
	if size < 0 {
		t.Fatalf("size should be >=0, got %d", size)
	}
	_ = used
}

func TestPackageRemainUsed_NegativeRemainBranch3(t *testing.T) {
	a := resourcePackage{CapacityRemain: -3, CapacityUsed: 7, CapacitySize: 10}
	remain, used, size := packageRemainUsed(a)
	t.Logf("branch3 remain=%d used=%d size=%d", remain, used, size)
	if remain < 0 {
		t.Fatalf("remain should be clamped >=0, got %d", remain)
	}
	_ = used
	_ = size
}
