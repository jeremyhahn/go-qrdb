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
