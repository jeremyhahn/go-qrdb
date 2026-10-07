// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// poolReadyValue reads qrdb_volume_pool_ready{pool}. It returns (0, false)
// when the series has never been recorded for pool.
func poolReadyValue(t *testing.T, pool string) (float64, bool) {
	t.Helper()
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "qrdb_volume_pool_ready" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "pool" && l.GetValue() == pool {
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

func TestRecordPoolHealth_ReadySetsOne(t *testing.T) {
	RecordPoolHealth("metrics-test-ready", Health{State: StateReady})
	v, ok := poolReadyValue(t, "metrics-test-ready")
	if !ok {
		t.Fatal("qrdb_volume_pool_ready{pool=\"metrics-test-ready\"} was never recorded")
	}
	if v != 1 {
		t.Fatalf("value = %v, want 1 for StateReady", v)
	}
}

func TestRecordPoolHealth_UnavailableSetsZero(t *testing.T) {
	RecordPoolHealth("metrics-test-unavailable", Health{State: StateUnavailable})
	v, ok := poolReadyValue(t, "metrics-test-unavailable")
	if !ok {
		t.Fatal("qrdb_volume_pool_ready{pool=\"metrics-test-unavailable\"} was never recorded")
	}
	if v != 0 {
		t.Fatalf("value = %v, want 0 for StateUnavailable", v)
	}
}

func TestRecordPoolHealth_LockedSetsZero(t *testing.T) {
	RecordPoolHealth("metrics-test-locked", Health{State: StateLocked})
	v, ok := poolReadyValue(t, "metrics-test-locked")
	if !ok {
		t.Fatal("qrdb_volume_pool_ready{pool=\"metrics-test-locked\"} was never recorded")
	}
	if v != 0 {
		t.Fatalf("value = %v, want 0 for StateLocked", v)
	}
}

// TestRecordPoolHealth_TransitionsBackToZero proves the gauge tracks the
// LATEST call for a pool, not just the first: a pool that opened ready and
// later becomes unavailable (or locks on re-seal) must flip back to 0, not
// stay stuck at 1.
func TestRecordPoolHealth_TransitionsBackToZero(t *testing.T) {
	RecordPoolHealth("metrics-test-transition", Health{State: StateReady})
	if v, ok := poolReadyValue(t, "metrics-test-transition"); !ok || v != 1 {
		t.Fatalf("after ready: value = %v, ok = %v, want 1, true", v, ok)
	}
	RecordPoolHealth("metrics-test-transition", Health{State: StateUnavailable})
	if v, ok := poolReadyValue(t, "metrics-test-transition"); !ok || v != 0 {
		t.Fatalf("after unavailable: value = %v, ok = %v, want 0, true", v, ok)
	}
}
