// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package database

import (
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

const (
	fsyncFamily     = "quicraft_logdb_fsync_latency_seconds"
	electionsFamily = "quicraft_raft_elections_total"
)

// electionSeries reports whether the shared collector exposes
// quicraft_raft_elections_total for shardID.
func electionSeries(t *testing.T, shardID uint64) bool {
	t.Helper()
	families, err := SharedRaftMetrics().Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := strconv.FormatUint(shardID, 10)
	for _, mf := range families {
		if mf.GetName() != electionsFamily {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "shard_id" && lp.GetValue() == want {
					return true
				}
			}
		}
	}
	return false
}

func TestNewRaftMetrics_SharesOneCollector(t *testing.T) {
	a, b := NewRaftMetrics(), NewRaftMetrics()
	if a.PrometheusMetricsCollector != b.PrometheusMetricsCollector {
		t.Fatal("two RaftMetrics wrap different go-quicraft collectors")
	}
	if a.PrometheusMetricsCollector != SharedRaftMetrics() {
		t.Fatal("RaftMetrics does not wrap SharedRaftMetrics")
	}
	if a.fsync == b.fsync {
		t.Fatal("two RaftMetrics share one fsync observer; each WAL needs its own attributes")
	}
}

func TestRaftMetricsRegisterShard_RefCountedAcrossHosts(t *testing.T) {
	const shard = 742001
	host1, host2 := NewRaftMetrics(), NewRaftMetrics()

	host1.RegisterShard(shard)
	host2.RegisterShard(shard)
	host1.IncElection(shard)
	if !electionSeries(t, shard) {
		t.Fatal("no election series after registration")
	}

	host1.UnregisterShard(shard)
	if !electionSeries(t, shard) {
		t.Fatal("first host's unregistration deleted the series the second host still reports to")
	}

	host2.UnregisterShard(shard)
	if electionSeries(t, shard) {
		t.Fatal("series survived the last unregistration")
	}
}

func TestRaftMetricsUnregisterShard_WithoutRegistrationIgnored(t *testing.T) {
	const shard = 742002
	m := NewRaftMetrics()
	m.UnregisterShard(shard)

	m.RegisterShard(shard)
	m.IncElection(shard)
	if !electionSeries(t, shard) {
		t.Fatal("a stray unregistration made the next registration a no-op")
	}
	m.UnregisterShard(shard)
}

func TestRaftMetricsObserveFsyncLatency_SlowWarnsOnceAndRecords(t *testing.T) {
	log := syncwatchtest.NewLog()
	m := NewRaftMetrics(syncwatch.WithLogger(log.Logger()), syncwatch.WithAttrs(slog.String("wal_dir", "/data/wal")))
	before, _ := syncwatchtest.HistogramSample(t, SharedRaftMetrics().Registry, fsyncFamily, "", "")

	m.ObserveFsyncLatency(3 * time.Second)

	after, _ := syncwatchtest.HistogramSample(t, SharedRaftMetrics().Registry, fsyncFamily, "", "")
	if after != before+1 {
		t.Fatalf("fsync sample count went %d -> %d, want +1", before, after)
	}
	recs := log.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d warnings, want exactly 1", len(recs))
	}
	r := recs[0]
	if r.Level != slog.LevelWarn || r.Attrs["took"] != 3*time.Second ||
		r.Attrs["threshold"] != syncwatch.DefaultSlowThreshold || r.Attrs["wal_dir"] != "/data/wal" {
		t.Fatalf("warning = %+v", r)
	}
}

func TestRaftMetricsObserveFsyncLatency_FastDoesNotWarn(t *testing.T) {
	log := syncwatchtest.NewLog()
	m := NewRaftMetrics(syncwatch.WithLogger(log.Logger()))

	m.ObserveFsyncLatency(2 * time.Millisecond)

	if n := len(log.Records()); n != 0 {
		t.Fatalf("got %d warnings for a 2ms fsync, want 0", n)
	}
}

// TestProcessSlowSyncThreshold_ReachesWALAndStorageObservers: the observers
// this package builds for the Raft WAL and for every storage backend follow
// the process threshold a node's boot sets from
// observability.slow_sync_threshold: under 50ms, a 100ms sync warns and a
// 10ms one does not.
func TestProcessSlowSyncThreshold_ReachesWALAndStorageObservers(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	log := captureDefaultLog(t)
	prev := syncwatch.SlowThreshold()
	t.Cleanup(func() { syncwatch.SetSlowThreshold(prev) })
	syncwatch.SetSlowThreshold(50 * time.Millisecond)

	wal := NewRaftMetrics(syncwatch.WithAttrs(slog.String("wal_dir", "/data/wal")))
	for _, observe := range []func(time.Duration){
		wal.ObserveFsyncLatency,
		storageSyncs(syncwatch.BackendFile, "orders", "/data/orders").Observe,
		storageSyncs(syncwatch.BackendPebble, "orders", "/data/orders").Observe,
	} {
		observe(10 * time.Millisecond)
		observe(100 * time.Millisecond)
	}

	recs := log.Records()
	if len(recs) != 3 {
		t.Fatalf("got %d warnings; want one per 100ms sync and none for a 10ms one", len(recs))
	}
	for _, r := range recs {
		if r.Attrs["threshold"] != 50*time.Millisecond || r.Attrs["took"] != 100*time.Millisecond {
			t.Fatalf("warning %q attrs = %v; want threshold 50ms took 100ms", r.Message, r.Attrs)
		}
	}
}
