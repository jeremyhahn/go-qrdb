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

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// scopeCall captures a single (service, tenant, database) resolution request.
type scopeCall struct {
	service string
	tenant  string
	db      string
}

// recordingRegistry implements service.Registry, capturing the (tenant,
// database) pair passed to each resolver and returning a preconfigured KV
// stub. Methods not exercised return nil.
type recordingRegistry struct {
	calls []scopeCall
	kv    service.KVServicer
}

func (r *recordingRegistry) record(svc, t, d string) {
	r.calls = append(r.calls, scopeCall{service: svc, tenant: t, db: d})
}

func (r *recordingRegistry) KVService(t, d string) (service.KVServicer, error) {
	r.record("kv", t, d)
	return r.kv, nil
}
func (r *recordingRegistry) MVCCService(t, d string) (service.MVCCKVServicer, error) {
	r.record("mvcc", t, d)
	return nil, nil
}
func (r *recordingRegistry) WatchService(t, d string) (service.WatchServicer, error) {
	r.record("watch", t, d)
	return nil, nil
}
func (r *recordingRegistry) LeaseService(t, d string) (lease.LeaseServicer, error) {
	r.record("lease", t, d)
	return nil, nil
}
func (r *recordingRegistry) IndexService(t, d string) (index.IndexServicer, error) {
	r.record("index", t, d)
	return nil, nil
}
func (r *recordingRegistry) TimeSeriesService(t, d string) (timeseries.TimeSeriesServicer, error) {
	r.record("timeseries", t, d)
	return nil, nil
}
func (r *recordingRegistry) VectorService(t, d string) (service.VectorServicer, error) {
	r.record("vector", t, d)
	return nil, nil
}
func (r *recordingRegistry) TransactionService(t, d string) (service.TransactionServicer, error) {
	r.record("transaction", t, d)
	return nil, nil
}
func (r *recordingRegistry) BatchService(t, d string) (service.BatchServicer, error) {
	r.record("batch", t, d)
	return nil, nil
}
func (r *recordingRegistry) TableService(t, d string) (service.TableServicer, error) {
	r.record("table", t, d)
	return nil, nil
}
func (r *recordingRegistry) SequenceService(t, d string) (service.SequenceServicer, error) {
	r.record("sequence", t, d)
	return nil, nil
}

func (r *recordingRegistry) PrivilegeService(t, d string) (service.PrivilegeServicer, error) {
	r.record("privilege", t, d)
	return nil, nil
}

func TestMCPResolveKVUsesRegistryScope(t *testing.T) {
	regKV := &mockKVService{}
	reg := &recordingRegistry{kv: regKV}

	ctx := withRegistry(context.Background(), reg)
	ctx = tenant.WithTenantID(ctx, "acme")
	ctx = tenant.WithDatabase(ctx, "db2")

	fallback := &mockKVService{}
	got, err := resolveKV(ctx, fallback)
	if err != nil {
		t.Fatalf("resolveKV: %v", err)
	}
	if got != service.KVServicer(regKV) {
		t.Errorf("resolveKV returned the fallback, want the registry stub")
	}
	if len(reg.calls) != 1 || reg.calls[0].tenant != "acme" || reg.calls[0].db != "db2" {
		t.Errorf("registry calls = %+v, want one (acme, db2) call", reg.calls)
	}
}

func TestMCPResolveKVFallbackWithoutRegistry(t *testing.T) {
	fallback := &mockKVService{}
	got, err := resolveKV(context.Background(), fallback)
	if err != nil {
		t.Fatalf("resolveKV: %v", err)
	}
	if got != service.KVServicer(fallback) {
		t.Errorf("resolveKV did not return the fallback when no registry is bound")
	}
}

func TestMCPResolveKVDefaultDatabaseScope(t *testing.T) {
	reg := &recordingRegistry{kv: &mockKVService{}}
	ctx := withRegistry(context.Background(), reg)
	ctx = tenant.WithTenantID(ctx, "acme") // no database scope

	if _, err := resolveKV(ctx, &mockKVService{}); err != nil {
		t.Fatalf("resolveKV: %v", err)
	}
	if len(reg.calls) != 1 || reg.calls[0].db != tenant.DefaultDatabase {
		t.Errorf("registry call db = %+v, want default %q", reg.calls, tenant.DefaultDatabase)
	}
}

func TestMCPDatabaseScopeMiddlewareBindsScope(t *testing.T) {
	reg := &recordingRegistry{}
	s := &Server{registry: reg}
	mw := s.databaseScopeMiddleware()

	var (
		gotDB  string
		gotReg service.Registry
	)
	next := func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		gotDB = tenant.DatabaseFromContext(ctx)
		gotReg = registryFromContext(ctx)
		return mcpgo.NewToolResultText("ok"), nil
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_get"
	req.Params.Arguments = map[string]any{"database": "db2"}

	if _, err := mw(mcpserver.ToolHandlerFunc(next))(context.Background(), req); err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if gotDB != "db2" {
		t.Errorf("database scope in ctx = %q, want db2", gotDB)
	}
	if gotReg != service.Registry(reg) {
		t.Errorf("registry not bound into ctx by middleware")
	}
}

func TestMCPDatabaseScopeMiddlewareDefaultsToSystem(t *testing.T) {
	s := &Server{registry: &recordingRegistry{}}
	mw := s.databaseScopeMiddleware()

	var gotDB string
	next := func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		gotDB = tenant.DatabaseFromContext(ctx)
		return mcpgo.NewToolResultText("ok"), nil
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_get" // no database argument

	if _, err := mw(mcpserver.ToolHandlerFunc(next))(context.Background(), req); err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if gotDB != tenant.DefaultDatabase {
		t.Errorf("database scope = %q, want default %q", gotDB, tenant.DefaultDatabase)
	}
}
