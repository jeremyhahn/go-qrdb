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
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
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
