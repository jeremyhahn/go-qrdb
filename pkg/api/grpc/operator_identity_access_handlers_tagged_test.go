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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd && !integration

package grpc

import (
	"context"
	"errors"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// TestDatabaseScale_Branches: no database service is refused, the service's
// error is mapped, and a scaled database is returned.
func TestDatabaseScale_Branches(t *testing.T) {
	ctx := context.Background()
	req := &pb.DatabaseScaleRequest{Name: "orders", TargetReplicas: 3}
	if _, err := (&qrdbServiceServer{}).DatabaseScale(ctx, req); err == nil {
		t.Fatal("DatabaseScale with no database service: want an error")
	}
	failing := &qrdbServiceServer{databaseSvc: &mockDatabaseService{updateErr: errors.New("scale refused")}}
	if _, err := failing.DatabaseScale(ctx, req); err == nil {
		t.Fatal("DatabaseScale with a failing service: want an error")
	}
	ok := &qrdbServiceServer{databaseSvc: &mockDatabaseService{updateResult: &database.DatabaseInfo{Name: "orders"}}}
	resp, err := ok.DatabaseScale(ctx, req)
	if err != nil || resp == nil {
		t.Fatalf("DatabaseScale: resp=%v err=%v, want a database", resp, err)
	}
}

// TestVectorUpdateNamespace_Branches: with no vector service the handler
// refuses; malformed index parameters or metadata are refused; a well-formed
// update reaches the vector service.
func TestVectorUpdateNamespace_Branches(t *testing.T) {
	ctx := context.Background()
	if _, err := (&qrdbServiceServer{}).VectorUpdateNamespace(ctx, &pb.VectorUpdateNamespaceRequest{Namespace: "n"}); err == nil {
		t.Fatal("VectorUpdateNamespace with no vector service: want an error")
	}
	srv := &qrdbServiceServer{vectorSvc: &mockVectorService{}}
	for _, bad := range []*pb.VectorUpdateNamespaceRequest{
		{Namespace: "n", IndexParamsJson: "{"},
		{Namespace: "n", MetadataJson: "{"},
	} {
		if _, err := srv.VectorUpdateNamespace(ctx, bad); err == nil {
			t.Fatalf("VectorUpdateNamespace(%v): want a parse error", bad)
		}
	}
	resp, err := srv.VectorUpdateNamespace(ctx, &pb.VectorUpdateNamespaceRequest{
		Namespace: "n", Dimension: 4, IndexParamsJson: `{"m":16}`, MetadataJson: `{"owner":"acme"}`,
	})
	if err != nil || !resp.GetUpdated() || !resp.GetRequiresRebuild() {
		t.Fatalf("VectorUpdateNamespace: resp=%v err=%v, want updated with a rebuild", resp, err)
	}
}
