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

// Deliberately unconstrained. The collection surface is served in every tag
// set this Makefile can produce, so the test that proves it resolves per
// (tenant, database) must compile in every one of them too. It therefore uses
// its own narrow stub rather than mocks_svc_test.go's full
// mockCollectionService, whose file carries the qrdb_ts/vector/mysql/pgsql/etcd
// constraint.

package quic

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopedCollectionStub is the narrowest CollectionServicer that can answer
// "which servicer did the handler reach?". Only CreateCollection is
// implemented; every other method comes from the embedded nil interface and
// panics if called, which is what keeps the stub honest about its scope.
type scopedCollectionStub struct {
	service.CollectionServicer
	name    string
	created []string
}

// CreateCollection records the database it was asked to create in and
// succeeds.
func (s *scopedCollectionStub) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	s.created = append(s.created, req.Database+"/"+req.Name)
	return &service.CollectionInfo{Name: req.Name, Database: req.Database}, nil
}

// TestQUICCollectionResolvesThroughTheRegistry holds the transport half of
// go-qrdb#441: with a registry configured, a collection frame must resolve the
// servicer for the caller's tenant and the database the FRAME names, and must
// not reach the process-wide singleton.
func TestQUICCollectionResolvesThroughTheRegistry(t *testing.T) {
	singleton := &scopedCollectionStub{name: "singleton"}
	scoped := &scopedCollectionStub{name: "scoped"}
	reg := &recordingRegistry{coll: scoped}
	s := &Server{registry: reg, collectionSvc: singleton}

	payload, err := proto.Marshal(&pb.CreateCollectionRequest{Database: "db2", Name: "users"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	if _, err := s.handleCollectionCreate(ctx, payload); err != nil {
		t.Fatalf("handleCollectionCreate: %v", err)
	}

	if len(singleton.created) != 0 {
		t.Fatalf("the process-wide singleton was used: %v", singleton.created)
	}
	if len(scoped.created) != 1 || scoped.created[0] != "db2/users" {
		t.Fatalf("scoped servicer saw %v, want [db2/users]", scoped.created)
	}
	if len(reg.calls) != 1 {
		t.Fatalf("registry calls = %v, want exactly one", reg.calls)
	}
	got := reg.calls[0]
	if got.service != "collection" || got.tenant != "acme" || got.db != "db2" {
		t.Fatalf("registry resolved %+v, want {collection acme db2}", got)
	}
}

// TestQUICCollectionFallsBackToTheSingleton covers the other half of the
// contract: with no registry the singleton is still served, which is the
// single-tenant composition where the whole store and the caller's own
// keyspace are the same set.
func TestQUICCollectionFallsBackToTheSingleton(t *testing.T) {
	singleton := &scopedCollectionStub{name: "singleton"}
	s := &Server{collectionSvc: singleton}

	payload, err := proto.Marshal(&pb.CreateCollectionRequest{Database: "db2", Name: "users"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.handleCollectionCreate(context.Background(), payload); err != nil {
		t.Fatalf("handleCollectionCreate: %v", err)
	}
	if len(singleton.created) != 1 || singleton.created[0] != "db2/users" {
		t.Fatalf("singleton saw %v, want [db2/users]", singleton.created)
	}
}

// TestQUICCollectionWithNeitherIsRefused pins the fail-closed case: no
// registry and no singleton is a configuration error reported as such, not a
// nil dereference.
func TestQUICCollectionWithNeitherIsRefused(t *testing.T) {
	s := &Server{}
	if _, err := s.collectionSvcFor(context.Background(), "db2"); err == nil {
		t.Fatal("expected an error when neither a registry nor a singleton is configured")
	} else if _, ok := err.(*ServiceNotConfiguredError); !ok {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}
