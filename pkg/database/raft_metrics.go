// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package database

import (
	"sync"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
)

// raftMetrics is the process's one go-quicraft metrics collector and its
// shard reference counts.
//
// A process holds one go-quicraft PrometheusMetricsCollector, created on
// first use, whatever the number of hosts or nodes in it: the SDK runs
// several nodes in one test process, and a collector per node would either
// collide on one registry or leave all but one unscraped. Every RaftMetrics
// NewRaftMetrics returns shares it, so the quicraft_* series are the
// process's aggregate, the way the Go runtime series already are.
//
// Sharing has one hazard. go-quicraft deletes a shard's labelled series when
// a host unregisters that shard, and two hosts in one process can run the
// same shard ID (the SDK's nodes are replicas of the same shards). The shard
// registrations are therefore reference-counted: the shared collector sees
// RegisterShard on a shard's first registration and UnregisterShard on its
// last.
var raftMetrics = struct {
	once      sync.Once
	collector *quicraft.PrometheusMetricsCollector
	mu        sync.Mutex
	shards    map[uint64]int
}{shards: make(map[uint64]int)}

// SharedRaftMetrics returns the process's go-quicraft metrics collector,
// whose Registry holds the quicraft_* series every Raft host in the process
// reports to. /metrics serves it beside the process registry.
func SharedRaftMetrics() *quicraft.PrometheusMetricsCollector {
	raftMetrics.once.Do(func() {
		raftMetrics.collector = quicraft.NewPrometheusMetricsCollector()
	})
	return raftMetrics.collector
}

// RaftMetrics is the quicraft.MetricsCollector a qrdb Raft host reports to:
// the shared go-quicraft collector, with reference-counted shard
// registration and a slow-fsync warning on the WAL fsync path. Every
// duration that reaches ObserveFsyncLatency is recorded by go-quicraft and
// then checked against the syncwatch threshold, so a slow WAL fsync logs a
// warning naming the WAL it came from.
type RaftMetrics struct {
	*quicraft.PrometheusMetricsCollector
	fsync *syncwatch.Observer
}

var _ quicraft.MetricsCollector = (*RaftMetrics)(nil)

// NewRaftMetrics returns a RaftMetrics over the process's shared go-quicraft
// collector. opts configure its slow-fsync warning: WithAttrs names the WAL
// the warnings come from, WithLogger where they go.
func NewRaftMetrics(opts ...syncwatch.Option) *RaftMetrics {
	return &RaftMetrics{
		PrometheusMetricsCollector: SharedRaftMetrics(),
		fsync:                      syncwatch.ForWAL(opts...),
	}
}

// ObserveFsyncLatency records one WAL fsync in
// quicraft_logdb_fsync_latency_seconds and warns when it was slow.
func (m *RaftMetrics) ObserveFsyncLatency(d time.Duration) {
	m.PrometheusMetricsCollector.ObserveFsyncLatency(d)
	m.fsync.Observe(d)
}

// RegisterShard registers shardID with the shared collector on its first
// registration in the process and counts the others.
func (m *RaftMetrics) RegisterShard(shardID uint64) {
	raftMetrics.mu.Lock()
	defer raftMetrics.mu.Unlock()
	raftMetrics.shards[shardID]++
	if raftMetrics.shards[shardID] == 1 {
		m.PrometheusMetricsCollector.RegisterShard(shardID)
	}
}

// UnregisterShard drops one registration of shardID, and unregisters it from
// the shared collector, deleting its series, when it was the last. An
// unregistration without a matching registration is ignored.
func (m *RaftMetrics) UnregisterShard(shardID uint64) {
	raftMetrics.mu.Lock()
	defer raftMetrics.mu.Unlock()
	n, ok := raftMetrics.shards[shardID]
	if !ok {
		return
	}
	if n > 1 {
		raftMetrics.shards[shardID] = n - 1
		return
	}
	delete(raftMetrics.shards, shardID)
	m.PrometheusMetricsCollector.UnregisterShard(shardID)
}
