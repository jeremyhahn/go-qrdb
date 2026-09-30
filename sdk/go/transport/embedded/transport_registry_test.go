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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// collectionRegistry is a service.Registry that answers CollectionService
// and records the scope it was asked for. Every other method is the
// embedded nil interface: calling one fails the test loudly.
type collectionRegistry struct {
	service.Registry
	svc      service.CollectionServicer
	err      error
	tenantID string
	database string
}

func (r *collectionRegistry) CollectionService(tenantID, db string) (service.CollectionServicer, error) {
	r.tenantID, r.database = tenantID, db
	return r.svc, r.err
}

// A named database reached through the embedded transport resolves through
// the same per-(tenant, database) registry the network transports use, and
// the singleton collection service is not consulted at all.
func TestEmbeddedCollection_NamedDatabaseResolvesThroughTheRegistry(t *testing.T) {
	scoped := &mockCollectionService{}
	reg := &collectionRegistry{svc: scoped}
	singletonCalled := false
	singleton := &mockCollectionService{
		createFn: func(context.Context, *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			singletonCalled = true
			return &service.CollectionInfo{}, nil
		},
	}
	tr := newExtTransport(t, WithCollectionService(singleton), WithServiceRegistry(reg))
	ctx := tenant.WithTenantID(context.Background(), "acme")

	if _, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{Database: "orders", Name: "users"}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if singletonCalled {
		t.Fatal("the singleton collection service was used for a named database")
	}
	if reg.tenantID != "acme" || reg.database != "orders" {
		t.Fatalf("resolved (%q, %q), want (acme, orders)", reg.tenantID, reg.database)
	}

	for name, call := range map[string]func() error{
		"GetCollectionInfo": func() error { _, err := tr.GetCollectionInfo(ctx, "orders", "users"); return err },
		"ListCollections":   func() error { _, err := tr.ListCollections(ctx, "orders"); return err },
		"DropCollection":    func() error { return tr.DropCollection(ctx, "orders", "users") },
		"InsertDocument": func() error {
			_, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "orders", Collection: "users"})
			return err
		},
		"GetDocument": func() error { _, err := tr.GetDocument(ctx, "orders", "users", 1); return err },
		"UpdateDocument": func() error {
			return tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{Database: "orders", Collection: "users", ID: 1})
		},
		"DeleteDocument": func() error { return tr.DeleteDocument(ctx, "orders", "users", 1) },
		"ListDocuments": func() error {
			_, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "orders", Collection: "users"})
			return err
		},
	} {
		reg.database = ""
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if reg.database != "orders" {
			t.Fatalf("%s did not resolve through the registry", name)
		}
	}
}

// A resolution failure -- the database is being dropped, or does not exist
// for this tenant -- is the answer, exactly as on the network transports;
// nothing falls back to the singleton.
func TestEmbeddedCollection_RegistryResolutionFailureIsReturned(t *testing.T) {
	dropping := &database.DroppingError{Name: "orders"}
	reg := &collectionRegistry{err: dropping}
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}), WithServiceRegistry(reg))

	_, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{Database: "orders", Collection: "users"})
	if !errors.Is(err, dropping) {
		t.Fatalf("error = %v, want the registry's *database.DroppingError", err)
	}
}

// With no registry configured, the singleton serves, as it does on every
// network transport.
func TestEmbeddedCollection_NilRegistryUsesTheSingleton(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	if _, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{Database: "db", Name: "users"}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	bare := newExtTransport(t)
	var nilSvc *NilServiceError
	if _, err := bare.CreateCollection(context.Background(), transport.CreateCollectionRequest{Database: "db", Name: "users"}); !errors.As(err, &nilSvc) {
		t.Fatalf("error = %v, want *NilServiceError", err)
	}
}

func TestEmbeddedTransport_ServiceRegistryProvider(t *testing.T) {
	if got := newExtTransport(t).registry; got != nil {
		t.Fatalf("registry without WithServiceRegistry = %v, want nil", got)
	}
	reg := &collectionRegistry{}
	if got := newExtTransport(t, WithServiceRegistry(reg)).registry; got != reg {
		t.Fatal("WithServiceRegistry did not set the transport's registry")
	}
}

// The default database cannot be dropped through the embedded transport:
// it is the DatabaseServicer's refusal, typed, exactly as on the network.
func TestEmbeddedDatabase_DropOfTheDefaultDatabaseIsRefused(t *testing.T) {
	dbSvc := service.NewDatabaseService(database.NewManager(nil, t.TempDir()))
	tr := newExtTransport(t, WithDatabaseService(dbSvc))

	var refused *database.DefaultDatabaseDropError
	if err := tr.DropDatabase(context.Background(), tenant.DefaultDatabase); !errors.As(err, &refused) {
		t.Fatalf("DropDatabase(%q) error = %v (%T), want *database.DefaultDatabaseDropError", tenant.DefaultDatabase, err, err)
	}
}
