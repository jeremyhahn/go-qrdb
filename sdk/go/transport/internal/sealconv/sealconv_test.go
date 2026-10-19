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

package sealconv

import (
	"reflect"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestFromProto(t *testing.T) {
	if got := FromProto(nil); *got != (transport.ClusterSeal{}) {
		t.Fatalf("nil = %+v", *got)
	}
	in := &pb.ClusterSealState{Active: true, Sealed: true, Epoch: 7, Principal: "ops",
		PrincipalTenant: "system", Node: "192.0.2.1:5000", At: "2026-10-01T12:00:00Z", Local: "held"}
	want := transport.ClusterSeal{Active: true, Sealed: true, Epoch: 7, Principal: "ops",
		PrincipalTenant: "system", Node: "192.0.2.1:5000", At: "2026-10-01T12:00:00Z", Local: "held"}
	if got := FromProto(in); *got != want {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
}

func TestFromReport(t *testing.T) {
	if got := FromReport(nil); *got != (transport.ClusterSeal{}) {
		t.Fatalf("nil = %+v", *got)
	}
	if got := FromReport(&clusterseal.Report{}); got.At != "" {
		t.Fatalf("a zero time rendered %q", got.At)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	got := FromReport(&clusterseal.Report{Active: true, Sealed: true, Epoch: 3, Principal: "alice",
		PrincipalTenant: "acme", Node: "192.0.2.2:5000", At: at, Local: clusterseal.LocalSealed})
	want := transport.ClusterSeal{Active: true, Sealed: true, Epoch: 3, Principal: "alice",
		PrincipalTenant: "acme", Node: "192.0.2.2:5000", At: "2026-10-01T12:00:00Z", Local: "sealed"}
	if *got != want {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
}

// wantMigration is the transport form of the migration both converters below
// are fed.
var wantMigration = &transport.ClusterMigration{
	Target: "passphrase", Generation: 2, Acked: 1, Total: 2,
	Nodes: []transport.ClusterMigrationNode{
		{NodeID: "n2", Step: "serving", Phase: "complete"},
		{NodeID: "n1", Step: "recycling", Phase: "failed", Attempts: 3, Detail: "credential missing"},
	},
}

func TestMigrationFromProto(t *testing.T) {
	if got := MigrationFromProto(nil); got != nil {
		t.Fatalf("nil = %+v, want nil: no migration in progress", got)
	}
	if got := MigrationFromProto(&pb.ClusterMigrationState{Unconfirmed: true}); !got.Unconfirmed || got.Nodes != nil {
		t.Fatalf("unconfirmed = %+v", got)
	}
	got := MigrationFromProto(&pb.ClusterMigrationState{
		Target: "passphrase", Generation: 2, Acked: 1, Total: 2,
		Nodes: []*pb.ClusterMigrationNode{
			{NodeId: "n2", Step: "serving", Phase: "complete"},
			{NodeId: "n1", Step: "recycling", Phase: "failed", Attempts: 3, Detail: "credential missing"},
		},
	})
	if !reflect.DeepEqual(got, wantMigration) {
		t.Fatalf("got %+v, want %+v", got, wantMigration)
	}
}

func TestMigrationFromReport(t *testing.T) {
	if got := MigrationFromReport(nil); got != nil {
		t.Fatalf("nil = %+v, want nil: no migration in progress", got)
	}
	if got := MigrationFromReport(&clustermig.Report{Unconfirmed: true}); !got.Unconfirmed || got.Nodes != nil {
		t.Fatalf("unconfirmed = %+v", got)
	}
	got := MigrationFromReport(&clustermig.Report{
		Target: seal.StrategyPassphrase, Generation: 2, Acked: 1, Total: 2,
		Nodes: []clustermig.NodeReport{
			{NodeID: "n2", Step: clustermig.StepServing, Phase: seal.PhaseComplete},
			{NodeID: "n1", Step: clustermig.StepRecycling, Phase: seal.PhaseFailed, Attempts: 3, Detail: "credential missing"},
		},
	})
	if !reflect.DeepEqual(got, wantMigration) {
		t.Fatalf("got %+v, want %+v", got, wantMigration)
	}
}
