// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Cluster sealing: barrier cluster-unseal and the cluster seal in status
// ---------------------------------------------------------------------------

func TestBarrierClusterUnsealCmd_PrintsTheClearedRecord(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	root := newBarrierTestRoot(t, &mockBarrierService{
		clusterUnsealFn: func(context.Context) (*clusterseal.Report, error) {
			return &clusterseal.Report{Active: true, Epoch: 4, Principal: "ops", PrincipalTenant: "system",
				Node: "192.0.2.1:5000", At: at, Local: clusterseal.LocalServing}, nil
		},
	})
	out, err := executeCommand(root, "barrier", "cluster-unseal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"Epoch:", "4", "serving", "ops"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output: %q", want, out)
		}
	}
	out, err = executeCommand(newBarrierTestRoot(t, &mockBarrierService{
		clusterUnsealFn: func(context.Context) (*clusterseal.Report, error) {
			return &clusterseal.Report{Active: true, Epoch: 4}, nil
		},
	}), "--output", "json", "barrier", "cluster-unseal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"epoch": 4`) && !strings.Contains(out, `"epoch":4`) {
		t.Errorf("expected epoch in JSON output: %q", out)
	}
}

func TestBarrierClusterUnsealCmd_InactiveIsAnError(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	if _, err := executeCommand(root, "barrier", "cluster-unseal"); err == nil {
		t.Fatal("cluster-unseal succeeded before cluster sealing is active")
	}
}

func TestBarrierStatusCmd_ShowsTheClusterSeal(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{
		statusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Strategy: "passphrase",
				ClusterSeal: &clusterseal.Report{Active: true, Sealed: true, Epoch: 7, Local: clusterseal.LocalHeld}}, nil
		},
	})
	out, err := executeCommand(root, "barrier", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"ClusterSeal:", "sealed", "epoch 7", "held"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in status output: %q", want, out)
		}
	}
	inactive := newBarrierTestRoot(t, &mockBarrierService{})
	out, err = executeCommand(inactive, "barrier", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "node-local") {
		t.Errorf("expected the node-local note when cluster sealing is inactive: %q", out)
	}
}

func TestBarrierStatusCmd_ShowsTheClusterMigration(t *testing.T) {
	rolling := &clustermig.Report{
		Target: seal.StrategyPassphrase, Generation: 2, Acked: 1, Total: 3,
		Nodes: []clustermig.NodeReport{
			{NodeID: "n3", Step: clustermig.StepServing, Phase: seal.PhaseComplete},
			{NodeID: "n1", Step: clustermig.StepRecycling, Phase: seal.PhaseFailed, Attempts: 2, Detail: "credential missing"},
			{NodeID: "n2", Step: clustermig.StepPending},
		},
	}
	for _, tc := range []struct {
		name string
		mig  *clustermig.Report
		want []string
	}{
		{name: "none in progress", want: []string{"Migration:", "none in progress"}},
		{name: "unconfirmed", mig: &clustermig.Report{Unconfirmed: true}, want: []string{"Migration:", "unconfirmed"}},
		{name: "rolling", mig: rolling, want: []string{
			"to passphrase, generation 2, 1 of 3 owners done",
			"n3             serving    complete",
			"n1             recycling  failed, attempt 2: credential missing",
			"n2             pending    not reported",
		}},
		{name: "pre-gate request has no steps", mig: &clustermig.Report{
			Target: seal.StrategyPassphrase, Generation: 1, Total: 1, Nodes: []clustermig.NodeReport{{NodeID: "n1"}},
		}, want: []string{"n1             -          not reported"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newBarrierTestRoot(t, &mockBarrierService{
				statusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
					return &service.BarrierStatusInfo{Strategy: "none", ClusterMigration: tc.mig}, nil
				},
			})
			out, err := executeCommand(root, "barrier", "status")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("expected %q in status output: %q", want, out)
				}
			}
		})
	}
}

func TestBarrierStatusCmd_JSONCarriesTheClusterMigration(t *testing.T) {
	status := func(mig *clustermig.Report) *cobra.Command {
		return newBarrierTestRoot(t, &mockBarrierService{
			statusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
				return &service.BarrierStatusInfo{Strategy: "none", ClusterMigration: mig}, nil
			},
		})
	}
	out, err := executeCommand(status(&clustermig.Report{
		Target: seal.StrategyPassphrase, Generation: 2, Acked: 1, Total: 1,
		Nodes: []clustermig.NodeReport{{NodeID: "n1", Step: clustermig.StepServing, Phase: seal.PhaseComplete}},
	}), "--output", "json", "barrier", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		ClusterMigration *transport.ClusterMigration `json:"cluster_migration"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\noutput: %s", err, out)
	}
	want := &transport.ClusterMigration{Target: "passphrase", Generation: 2, Acked: 1, Total: 1,
		Nodes: []transport.ClusterMigrationNode{{NodeID: "n1", Step: "serving", Phase: "complete"}}}
	if !reflect.DeepEqual(got.ClusterMigration, want) {
		t.Errorf("cluster_migration = %+v, want %+v", got.ClusterMigration, want)
	}
	out, err = executeCommand(status(nil), "--output", "json", "barrier", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "cluster_migration") {
		t.Errorf("no migration in progress carries cluster_migration: %q", out)
	}
}
