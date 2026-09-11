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

package mcp

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopedCollectionStub is the narrowest CollectionServicer that can answer
// "which servicer did the tool reach?". Only CreateCollection is implemented;
// the rest come from the embedded nil interface.
type scopedCollectionStub struct {
	service.CollectionServicer
	created []string
}

// CreateCollection records the database it was asked to create in.
func (s *scopedCollectionStub) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	s.created = append(s.created, req.Database+"/"+req.Name)
	return &service.CollectionInfo{Name: req.Name, Database: req.Database}, nil
}

// TestMCPCollectionResolvesThroughTheRegistry holds the transport half of
// go-qrdb#441 on MCP: with a registry bound to the call context, a collection
// tool must resolve the servicer for the caller's tenant and the database the
// TOOL ARGUMENT names, and must not reach the captured singleton.
func TestMCPCollectionResolvesThroughTheRegistry(t *testing.T) {
	singleton := &scopedCollectionStub{}
	scoped := &scopedCollectionStub{}
	reg := &recordingRegistry{coll: scoped}

	ctx := withRegistry(tenant.WithTenantID(context.Background(), "acme"), reg)
	req := buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database": "db2",
		"name":     "users",
	})

	if _, err := handleCollectionCreate(singleton)(ctx, req); err != nil {
		t.Fatalf("handleCollectionCreate: %v", err)
	}

	if len(singleton.created) != 0 {
		t.Fatalf("the captured singleton was used: %v", singleton.created)
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

// TestMCPCollectionFallsBackToTheSingleton covers the registry-less path,
// which is the single-database, embedded and unit-test composition.
func TestMCPCollectionFallsBackToTheSingleton(t *testing.T) {
	singleton := &scopedCollectionStub{}
	req := buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database": "db2",
		"name":     "users",
	})

	if _, err := handleCollectionCreate(singleton)(context.Background(), req); err != nil {
		t.Fatalf("handleCollectionCreate: %v", err)
	}
	if len(singleton.created) != 1 || singleton.created[0] != "db2/users" {
		t.Fatalf("singleton saw %v, want [db2/users]", singleton.created)
	}
}

// TestMCPCollectionWithNeitherIsRefused pins the fail-closed case. The tools
// are registered when EITHER a singleton or a registry is present, so a tool
// invoked without the scope middleware and without a singleton must report
// the misconfiguration rather than dereference nil.
func TestMCPCollectionWithNeitherIsRefused(t *testing.T) {
	if _, err := resolveCollection(context.Background(), nil, "db2"); err == nil {
		t.Fatal("expected an error when neither a registry nor a singleton is available")
	} else if _, ok := err.(*ServiceNotConfiguredError); !ok {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}
