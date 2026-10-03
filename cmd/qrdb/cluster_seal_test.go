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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
