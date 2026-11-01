// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

//go:build !integration

package node

import (
	"context"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

// The SDK boot path builds its Raft host through database.NewRaftWAL, as
// qrdbd does; a committed write on a clustered SDK node must therefore land
// in the WAL fsync histogram of the shared collector the server's default
// /metrics gatherer serves.
func TestClusteredNode_WALFsyncReachesMetricsGatherer(t *testing.T) {
	const fsyncFamily = "quicraft_logdb_fsync_latency_seconds"
	for _, c := range []struct {
		name   string
		sealed bool
	}{
		{"seal disabled", false},
		{"sealed", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, _ := singleMemberClustered(t, c.sealed)
			ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
			defer cancel()
			if err := putUntilCommitted(ctx, n.Client().KV(), "metrics/probe", []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			before, _ := syncwatchtest.HistogramSample(t, database.SharedRaftMetrics().Registry, fsyncFamily, "", "")
			if err := putUntilCommitted(ctx, n.Client().KV(), "metrics/probe", []byte("w")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			after, _ := syncwatchtest.HistogramSample(t, database.SharedRaftMetrics().Registry, fsyncFamily, "", "")
			if after <= before {
				t.Fatalf("%s sample count %d -> %d across a committed write", fsyncFamily, before, after)
			}
		})
	}
}

// A clustered SDK node's Raft host reports to the same shared collector: a
// committed write lands in the proposal latency histogram, a linearizable read
// in the ReadIndex one, and the election counter is bound for the node's
// shard, so the server's default /metrics gatherer serves all three.
func TestClusteredNode_ProposalsAndReadIndexReachMetricsGatherer(t *testing.T) {
	const (
		proposalFamily  = "quicraft_raft_proposal_latency_seconds"
		readIndexFamily = "quicraft_raft_read_index_latency_seconds"
		electionFamily  = "quicraft_raft_elections_total"
	)
	n, _ := singleMemberClustered(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()
	if err := putUntilCommitted(ctx, n.Client().KV(), "metrics/probe", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	registry := database.SharedRaftMetrics().Registry
	if !syncwatchtest.HasFamily(t, registry, electionFamily) {
		t.Fatalf("shared collector does not serve %s", electionFamily)
	}

	// go-quicraft records a request's latency on its own path once the
	// request completes, and nothing orders that observation before the
	// caller's result returns: the read-index sample is observed after the
	// reader is woken (once pendingReadsMu is released), and CI run 70098
	// read the proposal count unchanged (701 -> 701) just after Put returned.
	// Wait for each sample through the histogram, within the test's budget.
	waitForSample := func(family string, before uint64, across string) {
		t.Helper()
		after, _ := syncwatchtest.HistogramSample(t, registry, family, "", "")
		for after <= before {
			select {
			case <-ctx.Done():
				t.Fatalf("%s sample count %d -> %d across %s: %v", family, before, after, across, ctx.Err())
			case <-time.After(clusterPollInterval):
			}
			after, _ = syncwatchtest.HistogramSample(t, registry, family, "", "")
		}
	}

	proposalsBefore, _ := syncwatchtest.HistogramSample(t, registry, proposalFamily, "", "")
	if err := putUntilCommitted(ctx, n.Client().KV(), "metrics/probe", []byte("w")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	waitForSample(proposalFamily, proposalsBefore, "a committed write")

	readsBefore, _ := syncwatchtest.HistogramSample(t, registry, readIndexFamily, "", "")
	if err := readIndexOnce(ctx, n.host.(*quicraft.Host)); err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	waitForSample(readIndexFamily, readsBefore, "a linearizable read")
}
