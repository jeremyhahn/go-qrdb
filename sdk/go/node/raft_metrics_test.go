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

	proposalsBefore, _ := syncwatchtest.HistogramSample(t, registry, proposalFamily, "", "")
	if err := putUntilCommitted(ctx, n.Client().KV(), "metrics/probe", []byte("w")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if after, _ := syncwatchtest.HistogramSample(t, registry, proposalFamily, "", ""); after <= proposalsBefore {
		t.Fatalf("%s sample count %d -> %d across a committed write", proposalFamily, proposalsBefore, after)
	}

	readsBefore, _ := syncwatchtest.HistogramSample(t, registry, readIndexFamily, "", "")
	if err := readIndexOnce(n.host.(*quicraft.Host)); err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if after, _ := syncwatchtest.HistogramSample(t, registry, readIndexFamily, "", ""); after <= readsBefore {
		t.Fatalf("%s sample count %d -> %d across a linearizable read", readIndexFamily, readsBefore, after)
	}
}
