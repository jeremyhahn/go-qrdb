// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0

package quota

import "testing"

// TestEntitlements_AllowsVolumePool_Explicit covers membership and absence
// against a non-empty AllowedVolumePools.
func TestEntitlements_AllowsVolumePool_Explicit(t *testing.T) {
	e := &Entitlements{AllowedVolumePools: []string{"fast", "bulk"}}

	if !e.AllowsVolumePool("fast") {
		t.Error("AllowsVolumePool(fast) = false, want true")
	}
	if !e.AllowsVolumePool("bulk") {
		t.Error("AllowsVolumePool(bulk) = false, want true")
	}
	if e.AllowsVolumePool("vault") {
		t.Error("AllowsVolumePool(vault) = true, want false")
	}
}

// TestEntitlements_AllowsVolumePool_FailClosed covers the empty-list and
// empty-name cases: unlike AllowsBarrierMode, an empty AllowedVolumePools
// entitles no pool, since there is no software default pool.
func TestEntitlements_AllowsVolumePool_FailClosed(t *testing.T) {
	var zero Entitlements
	if zero.AllowsVolumePool("fast") {
		t.Error("AllowsVolumePool on a zero-value Entitlements = true, want false")
	}

	e := &Entitlements{AllowedVolumePools: []string{"fast"}}
	if e.AllowsVolumePool("") {
		t.Error("AllowsVolumePool(\"\") = true, want false")
	}
}

// TestEntitlements_DefaultVolumePool_FirstEntryWins covers the ordering
// contract: the first entry of AllowedVolumePools is the default, not the
// lexicographically smallest or any other derived order.
func TestEntitlements_DefaultVolumePool_FirstEntryWins(t *testing.T) {
	e := &Entitlements{AllowedVolumePools: []string{"bulk", "fast"}}
	got, ok := e.DefaultVolumePool()
	if !ok {
		t.Fatal("DefaultVolumePool() ok = false, want true")
	}
	if got != "bulk" {
		t.Fatalf("DefaultVolumePool() = %q, want %q", got, "bulk")
	}
}

// TestEntitlements_DefaultVolumePool_NoneEntitled covers the entitled-to-
// nothing case a create without --pool must refuse.
func TestEntitlements_DefaultVolumePool_NoneEntitled(t *testing.T) {
	var e Entitlements
	got, ok := e.DefaultVolumePool()
	if ok {
		t.Fatalf("DefaultVolumePool() ok = true, want false (got %q)", got)
	}
	if got != "" {
		t.Fatalf("DefaultVolumePool() = %q, want empty string", got)
	}
}
