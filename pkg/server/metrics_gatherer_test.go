// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

const walFsyncFamily = "quicraft_logdb_fsync_latency_seconds"

func TestMetricsGatherer_ServesProcessAndQuicraftRegistries(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	syncwatch.ForStorage(syncwatch.BackendPebble).Observe(time.Millisecond)
	database.NewRaftMetrics().ObserveFsyncLatency(time.Millisecond)

	g := metricsGatherer()
	for _, name := range []string{walFsyncFamily, "qrdb_storage_sync_duration_seconds", "go_goroutines"} {
		if !syncwatchtest.HasFamily(t, g, name) {
			t.Fatalf("metricsGatherer does not expose %s", name)
		}
	}
}

func TestMetricsGatherer_FollowsRegistrySwap(t *testing.T) {
	g := metricsGatherer()

	syncwatchtest.FreshRegistry(t)
	metrics.NewCounter(metrics.CounterOpts{Namespace: "qrdb", Subsystem: "gatherer_test", Name: "swapped_total", Help: "test"}).Inc()

	if !syncwatchtest.HasFamily(t, g, "qrdb_gatherer_test_swapped_total") {
		t.Fatal("a gatherer built before SetRegistry does not serve the new registry")
	}
}

// The process registry carries the Go runtime and process collectors; the
// go-quicraft registry must not carry them too, or every scrape fails on a
// duplicate family.
func TestMetricsGatherer_NoDuplicateFamilies(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	database.NewRaftMetrics().ObserveFsyncLatency(time.Millisecond)

	families, err := metricsGatherer().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	seen := make(map[string]bool, len(families))
	for _, mf := range families {
		if seen[mf.GetName()] {
			t.Fatalf("family %s gathered twice", mf.GetName())
		}
		seen[mf.GetName()] = true
	}
	for _, name := range []string{walFsyncFamily, "go_goroutines", "process_open_fds"} {
		if !seen[name] {
			t.Fatalf("scrape lacks %s", name)
		}
	}
}
