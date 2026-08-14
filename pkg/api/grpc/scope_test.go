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

package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// recordingRegistry is a service.Registry stub that records the (tenant, db)
// pair it was asked to resolve and returns preconfigured services/errors.
type recordingRegistry struct {
	gotTenant string
	gotDB     string
	kv        service.KVServicer
	mvcc      service.MVCCKVServicer
	err       error
}

func (r *recordingRegistry) KVService(t, db string) (service.KVServicer, error) {
	r.gotTenant, r.gotDB = t, db
	if r.err != nil {
		return nil, r.err
	}
	return r.kv, nil
}

func (r *recordingRegistry) MVCCService(t, db string) (service.MVCCKVServicer, error) {
	r.gotTenant, r.gotDB = t, db
	if r.err != nil {
		return nil, r.err
	}
	return r.mvcc, nil
}

func (r *recordingRegistry) WatchService(t, db string) (service.WatchServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) LeaseService(t, db string) (service.LeaseServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) IndexService(t, db string) (service.IndexServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) TimeSeriesService(t, db string) (service.TimeSeriesServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) VectorService(t, db string) (service.VectorServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) TransactionService(t, db string) (service.TransactionServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) BatchService(t, db string) (service.BatchServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) TableService(t, db string) (service.TableServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) SequenceService(t, db string) (service.SequenceServicer, error) {
	return nil, r.err
}
func (r *recordingRegistry) PrivilegeService(t, db string) (service.PrivilegeServicer, error) {
	return nil, r.err
}

func TestDatabaseScopeUnaryInterceptor_DefaultWhenAbsent(t *testing.T) {
	var got string
	handler := func(ctx context.Context, _ any) (any, error) {
		got = databaseFromContext(ctx)
		return nil, nil
	}
	if _, err := DatabaseScopeUnaryInterceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler); err != nil {
		t.Fatalf("interceptor error: %v", err)
	}
	if got != DefaultDatabaseScope {
		t.Fatalf("expected default %q, got %q", DefaultDatabaseScope, got)
	}
}

func TestDatabaseScopeUnaryInterceptor_ReadsMetadata(t *testing.T) {
	var got string
	handler := func(ctx context.Context, _ any) (any, error) {
		got = databaseFromContext(ctx)
		return nil, nil
	}
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(MetadataDatabase, "db2"))
	if _, err := DatabaseScopeUnaryInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler); err != nil {
		t.Fatalf("interceptor error: %v", err)
	}
	if got != "db2" {
		t.Fatalf("expected db2, got %q", got)
	}
}

func TestKVSvcFor_RegistryResolvesTenantAndDatabase(t *testing.T) {
	mockKV := newMockKVService()
	reg := &recordingRegistry{kv: mockKV}
	s := &qrdbServiceServer{registry: reg}

	ctx := withDatabase(tenant.WithTenantID(context.Background(), "acme"), "db2")
	svc, err := s.kvSvcFor(ctx)
	if err != nil {
		t.Fatalf("kvSvcFor error: %v", err)
	}
	if svc != service.KVServicer(mockKV) {
		t.Fatal("expected registry-provided KV servicer")
	}
	if reg.gotTenant != "acme" || reg.gotDB != "db2" {
		t.Fatalf("registry resolved (%q,%q), want (acme,db2)", reg.gotTenant, reg.gotDB)
	}
}

func TestKVSvcFor_SingletonFallbackAndNil(t *testing.T) {
	// No registry, singleton present → returns singleton.
	mockKV := newMockKVService()
	s := &qrdbServiceServer{kvSvc: mockKV}
	svc, err := s.kvSvcFor(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc != service.KVServicer(mockKV) {
		t.Fatal("expected singleton KV servicer")
	}

	// No registry, no singleton → ServiceNotConfiguredError.
	empty := &qrdbServiceServer{}
	if _, err := empty.kvSvcFor(context.Background()); err == nil {
		t.Fatal("expected error when neither registry nor singleton set")
	} else {
		var snc *ServiceNotConfiguredError
		if !errors.As(err, &snc) {
			t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
		}
	}
}

func TestKVSvcFor_RegistryErrorPropagates(t *testing.T) {
	reg := &recordingRegistry{err: &service.DatabaseNotFoundError{TenantID: "acme", Database: "ghost"}}
	s := &qrdbServiceServer{registry: reg}
	ctx := withDatabase(tenant.WithTenantID(context.Background(), "acme"), "ghost")
	if _, err := s.kvSvcFor(ctx); err == nil {
		t.Fatal("expected DatabaseNotFoundError")
	} else {
		var dnf *service.DatabaseNotFoundError
		if !errors.As(err, &dnf) {
			t.Fatalf("expected DatabaseNotFoundError, got %T", err)
		}
	}
}
