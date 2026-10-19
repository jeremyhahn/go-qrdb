// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clustermig

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// scanErrKV reads records but fails every scan, so a migration is found and
// its node statuses are not.
type scanErrKV struct{ *memKV }

func (k scanErrKV) Scan(context.Context, string, func(string, []byte) error) error { return errBoom }

func TestStore_Report_NoMigration(t *testing.T) {
	r, err := NewStore(newMemKV()).Report(context.Background(), tid, db)
	if err != nil || r != nil {
		t.Fatalf("Report = %+v, %v; want nil, nil", r, err)
	}
}

func TestStore_Report_RollingOrderStepsAndPhases(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	if err := s.swapMigration(ctx, nil, &ClusterMigration{
		TenantID: tid, Database: db, Target: seal.StrategyPassphrase,
		Owners: []string{"n1", "n2", "n3"}, Generation: 2,
		Steps: []NodeStep{{NodeID: "n3", State: StepServing}, {NodeID: "n1", State: StepRecycling}, {NodeID: "n2", State: StepPending}},
	}); err != nil {
		t.Fatalf("swapMigration: %v", err)
	}
	ack(t, s, "n3", 2, seal.PhaseComplete)
	if err := s.PutNodeStatus(ctx, tid, db, &NodeMigrationStatus{NodeID: "n1", Generation: 2, Phase: seal.PhaseFailed, Attempts: 2, Detail: "credential missing"}); err != nil {
		t.Fatalf("PutNodeStatus: %v", err)
	}
	// n2's status belongs to an older request and must not be reported.
	ack(t, s, "n2", 1, seal.PhaseComplete)

	got, err := s.Report(ctx, tid, db)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	want := &Report{
		Target: seal.StrategyPassphrase, Generation: 2, Acked: 1, Total: 3,
		Nodes: []NodeReport{
			{NodeID: "n3", Step: StepServing, Phase: seal.PhaseComplete},
			{NodeID: "n1", Step: StepRecycling, Phase: seal.PhaseFailed, Attempts: 2, Detail: "credential missing"},
			{NodeID: "n2", Step: StepPending},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Report =\n %+v\nwant\n %+v", got, want)
	}
}

func TestStore_Report_PreGateRequestInOwnerOrder(t *testing.T) {
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n2", "n1"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	ack(t, s, "n2", 1, seal.PhaseComplete)

	got, err := s.Report(context.Background(), tid, db)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	want := &Report{
		Target: seal.StrategyPassphrase, Generation: 1, Acked: 2, Total: 2,
		Nodes: []NodeReport{{NodeID: "n2", Phase: seal.PhaseComplete}, {NodeID: "n1", Phase: seal.PhaseComplete}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Report = %+v, want %+v", got, want)
	}
	if acked, total, err := s.Progress(context.Background(), tid, db); err != nil || acked != 2 || total != 2 {
		t.Fatalf("Progress = %d/%d, %v; want 2/2", acked, total, err)
	}
}

func TestStore_Report_ReadErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := NewStore(errKV{err: errBoom}).Report(ctx, tid, db); !errors.Is(err, errBoom) {
		t.Errorf("Report on a failing KV = %v, want wrapped errBoom", err)
	}
	kv := scanErrKV{newMemKV()}
	seedMigration(t, NewStore(kv.memKV), 1, []string{"n1"})
	if r, err := NewStore(kv).Report(ctx, tid, db); !errors.Is(err, errBoom) || r != nil {
		t.Errorf("Report with a failing status scan = %+v, %v; want nil, wrapped errBoom", r, err)
	}
}
