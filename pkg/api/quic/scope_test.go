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

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// scopeCall captures a single (service, tenant, database) resolution request
// made against the recording registry.
type scopeCall struct {
	service string
	tenant  string
	db      string
}

// recordingRegistry implements service.Registry, capturing the (tenant,
// database) pair passed to each resolver and returning preconfigured stub
// servicers. Methods not exercised by a given test return nil.
type recordingRegistry struct {
	calls []scopeCall
	kv    service.KVServicer
	mvcc  service.MVCCKVServicer
	ts    timeseries.TimeSeriesServicer
	coll  service.CollectionServicer
	lock  service.LockServicer
}

func (r *recordingRegistry) record(svc, tenantID, db string) {
	r.calls = append(r.calls, scopeCall{service: svc, tenant: tenantID, db: db})
}

func (r *recordingRegistry) KVService(t, d string) (service.KVServicer, error) {
	r.record("kv", t, d)
	return r.kv, nil
}

func (r *recordingRegistry) MVCCService(t, d string) (service.MVCCKVServicer, error) {
	r.record("mvcc", t, d)
	return r.mvcc, nil
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
	return r.ts, nil
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

func (r *recordingRegistry) CollectionService(t, d string) (service.CollectionServicer, error) {
	r.record("collection", t, d)
	return r.coll, nil
}

func (r *recordingRegistry) LockService(t, d string) (service.LockServicer, error) {
	r.record("lock", t, d)
	return r.lock, nil
}

// scopedCtx builds a context carrying the given tenant and database scope,
// mirroring what the auth middleware and handleStream bind per request.
func scopedCtx(tenantID, db string) context.Context {
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	return tenant.WithDatabase(ctx, db)
}

func TestQUICResolversUseRegistryScope(t *testing.T) {
	kv := &mockKVService{}
	mvcc := &mockMVCCKVService{}
	ts := &mockTimeSeriesService{}
	reg := &recordingRegistry{kv: kv, mvcc: mvcc, ts: ts}
	s := &Server{registry: reg}

	ctx := scopedCtx("acme", "db2")

	gotKV, err := s.kvSvcFor(ctx)
	if err != nil {
		t.Fatalf("kvSvcFor: %v", err)
	}
	if gotKV != service.KVServicer(kv) {
		t.Errorf("kvSvcFor returned %p, want registry stub %p", gotKV, kv)
	}

	gotMVCC, err := s.mvccSvcFor(ctx)
	if err != nil {
		t.Fatalf("mvccSvcFor: %v", err)
	}
	if gotMVCC != service.MVCCKVServicer(mvcc) {
		t.Errorf("mvccSvcFor returned the wrong stub")
	}

	if _, err := s.tsSvcFor(ctx); err != nil {
		t.Fatalf("tsSvcFor: %v", err)
	}

	// Every resolver must have routed the authenticated tenant and the
	// per-request database scope to the registry.
	if len(reg.calls) != 3 {
		t.Fatalf("registry calls = %d, want 3", len(reg.calls))
	}
	for _, c := range reg.calls {
		if c.tenant != "acme" || c.db != "db2" {
			t.Errorf("call %+v: want tenant=acme db=db2", c)
		}
	}
}

func TestQUICResolversDefaultDatabaseScope(t *testing.T) {
	reg := &recordingRegistry{kv: &mockKVService{}}
	s := &Server{registry: reg}

	// Tenant bound, but no database scope on the context (empty frame field).
	ctx := tenant.WithTenantID(context.Background(), "acme")
	if _, err := s.kvSvcFor(ctx); err != nil {
		t.Fatalf("kvSvcFor: %v", err)
	}

	if len(reg.calls) != 1 {
		t.Fatalf("registry calls = %d, want 1", len(reg.calls))
	}
	if reg.calls[0].db != tenant.DefaultDatabase {
		t.Errorf("db = %q, want default %q", reg.calls[0].db, tenant.DefaultDatabase)
	}
}

func TestQUICResolversFallbackToSingleton(t *testing.T) {
	kv := &mockKVService{}
	s := &Server{kvSvc: kv} // no registry

	got, err := s.kvSvcFor(scopedCtx("acme", "db2"))
	if err != nil {
		t.Fatalf("kvSvcFor: %v", err)
	}
	if got != service.KVServicer(kv) {
		t.Errorf("kvSvcFor returned %p, want singleton %p", got, kv)
	}
}

func TestQUICResolversNotConfigured(t *testing.T) {
	s := &Server{} // neither registry nor singleton

	_, err := s.kvSvcFor(context.Background())
	if err == nil {
		t.Fatal("kvSvcFor expected ServiceNotConfiguredError, got nil")
	}
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("error type = %T, want *ServiceNotConfiguredError", err)
	}
}

// TestQUICResolverBranches drives every remaining per-request resolver
// through its three branches: the registry (with the request's scope), the
// singleton fallback, and the not-configured error.
func TestQUICResolverBranches(t *testing.T) {
	resolvers := map[string]struct {
		resolve   func(*Server, context.Context) (any, error)
		singleton func() (*Server, any)
	}{
		"transaction": {
			resolve: func(s *Server, c context.Context) (any, error) { return s.txSvcFor(c) },
			singleton: func() (*Server, any) {
				svc := &mockTransactionService{}
				return &Server{txSvc: svc}, svc
			},
		},
		"batch": {
			resolve: func(s *Server, c context.Context) (any, error) { return s.batchSvcFor(c) },
			singleton: func() (*Server, any) {
				svc := &mockBatchService{}
				return &Server{batchSvc: svc}, svc
			},
		},
		"lease": {
			resolve: func(s *Server, c context.Context) (any, error) { return s.leaseSvcFor(c) },
			singleton: func() (*Server, any) {
				svc := &mockLeaseService{}
				return &Server{leaseSvc: svc}, svc
			},
		},
		"index": {
			resolve: func(s *Server, c context.Context) (any, error) { return s.indexSvcFor(c) },
			singleton: func() (*Server, any) {
				svc := &mockIndexService{}
				return &Server{indexSvc: svc}, svc
			},
		},
		"vector": {
			resolve: func(s *Server, c context.Context) (any, error) { return s.vectorSvcFor(c) },
			singleton: func() (*Server, any) {
				svc := &mockVectorService{}
				return &Server{vectorSvc: svc}, svc
			},
		},
		"watch": {
			resolve: func(s *Server, c context.Context) (any, error) { return s.watchSvcFor(c) },
			singleton: func() (*Server, any) {
				svc := &mockWatchService{}
				return &Server{watchSvc: svc}, svc
			},
		},
		"mvcc": {
			resolve: func(s *Server, c context.Context) (any, error) { return s.mvccSvcFor(c) },
			singleton: func() (*Server, any) {
				svc := &mockMVCCKVService{}
				return &Server{mvccSvc: svc}, svc
			},
		},
		"timeseries": {
			resolve: func(s *Server, c context.Context) (any, error) { return s.tsSvcFor(c) },
			singleton: func() (*Server, any) {
				svc := &mockTimeSeriesService{}
				return &Server{tsSvc: svc}, svc
			},
		},
	}

	for name, r := range resolvers {
		t.Run(name+"/registry", func(t *testing.T) {
			reg := &recordingRegistry{}
			if _, err := r.resolve(&Server{registry: reg}, scopedCtx("acme", "db2")); err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if len(reg.calls) != 1 || reg.calls[0] != (scopeCall{service: name, tenant: "acme", db: "db2"}) {
				t.Fatalf("registry calls = %+v, want one %s call for acme/db2", reg.calls, name)
			}
		})
		t.Run(name+"/singleton", func(t *testing.T) {
			s, want := r.singleton()
			got, err := r.resolve(s, scopedCtx("acme", "db2"))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != want {
				t.Fatalf("resolve returned %p, want singleton %p", got, want)
			}
		})
		t.Run(name+"/notconfigured", func(t *testing.T) {
			_, err := r.resolve(&Server{}, context.Background())
			var snc *ServiceNotConfiguredError
			if !errors.As(err, &snc) {
				t.Fatalf("error type = %T, want *ServiceNotConfiguredError", err)
			}
		})
	}
}
