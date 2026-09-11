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

// Deliberately unconstrained, like scope_test.go beside it: the collection
// surface is served in every tag set, so the test that proves it resolves per
// (tenant, database) must compile in every one of them.

package grpc

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopedCollectionStub is the narrowest CollectionServicer that can answer
// "which servicer did the handler reach?". Only CreateCollection is
// implemented; the rest come from the embedded nil interface.
type scopedCollectionStub struct {
	service.CollectionServicer
	created []string
}

// CreateCollection records the database it was asked to create in.
func (s *scopedCollectionStub) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	s.created = append(s.created, req.Database+"/"+req.Name)
	return &service.CollectionInfo{Name: req.Name, Database: req.Database}, nil
}

// TestGRPCCollectionResolvesThroughTheRegistry holds the transport half of
// go-qrdb#441 on gRPC -- and therefore on the Unix socket transport, which
// registers this same service. The database comes from the request message,
// not from x-qrdb-database, because that is where every collection RPC names
// it.
func TestGRPCCollectionResolvesThroughTheRegistry(t *testing.T) {
	singleton := &scopedCollectionStub{}
	scoped := &scopedCollectionStub{}
	reg := &recordingRegistry{coll: scoped}
	s := &qrdbServiceServer{registry: reg, collectionSvc: singleton}

	ctx := tenant.WithTenantID(context.Background(), "acme")
	if _, err := s.CreateCollection(ctx, &pb.CreateCollectionRequest{
		Database: "db2",
		Name:     "users",
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	if len(singleton.created) != 0 {
		t.Fatalf("the process-wide singleton was used: %v", singleton.created)
	}
	if len(scoped.created) != 1 || scoped.created[0] != "db2/users" {
		t.Fatalf("scoped servicer saw %v, want [db2/users]", scoped.created)
	}
	if reg.gotTenant != "acme" || reg.gotDB != "db2" {
		t.Fatalf("registry resolved (%q, %q), want (acme, db2)", reg.gotTenant, reg.gotDB)
	}
}

// TestGRPCCollectionFallsBackToTheSingleton covers the registry-less
// composition, where the singleton is correct.
func TestGRPCCollectionFallsBackToTheSingleton(t *testing.T) {
	singleton := &scopedCollectionStub{}
	s := &qrdbServiceServer{collectionSvc: singleton}

	if _, err := s.CreateCollection(context.Background(), &pb.CreateCollectionRequest{
		Database: "db2",
		Name:     "users",
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if len(singleton.created) != 1 || singleton.created[0] != "db2/users" {
		t.Fatalf("singleton saw %v, want [db2/users]", singleton.created)
	}
}

// TestGRPCCollectionWithNeitherIsRefused pins the fail-closed case.
func TestGRPCCollectionWithNeitherIsRefused(t *testing.T) {
	s := &qrdbServiceServer{}
	if _, err := s.collectionSvcFor(context.Background(), "db2"); err == nil {
		t.Fatal("expected an error when neither a registry nor a singleton is configured")
	} else if _, ok := err.(*ServiceNotConfiguredError); !ok {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}
