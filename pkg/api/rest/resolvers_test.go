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

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// errServiceRegistry is a ServiceRegistry whose every lookup fails with err.
type errServiceRegistry struct {
	err error
}

var _ ServiceRegistry = (*errServiceRegistry)(nil)

func (r *errServiceRegistry) KVService(_, _ string) (service.KVServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) MVCCService(_, _ string) (service.MVCCKVServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) WatchService(_, _ string) (service.WatchServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) LeaseService(_, _ string) (service.LeaseServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) IndexService(_, _ string) (service.IndexServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) TimeSeriesService(_, _ string) (service.TimeSeriesServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) VectorService(_, _ string) (service.VectorServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) TransactionService(_, _ string) (service.TransactionServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) BatchService(_, _ string) (service.BatchServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) TableService(_, _ string) (service.TableServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) SequenceService(_, _ string) (service.SequenceServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) PrivilegeService(_, _ string) (service.PrivilegeServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) CollectionService(_, _ string) (service.CollectionServicer, error) {
	return nil, r.err
}
func (r *errServiceRegistry) LockService(_, _ string) (service.LockServicer, error) {
	return nil, r.err
}

// TestSvcFor_RegistryError asserts every resolver writes the registry
// error's status and returns no service.
func TestSvcFor_RegistryError(t *testing.T) {
	reg := &errServiceRegistry{err: &service.KeyNotFoundError{Key: "x"}}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithServiceRegistry(reg))
	resolvers := map[string]func(http.ResponseWriter) bool{
		"kv":    func(w http.ResponseWriter) bool { return s.kvSvcFor(w, context.Background()) == nil },
		"mvcc":  func(w http.ResponseWriter) bool { return s.mvccSvcFor(w, context.Background()) == nil },
		"watch": func(w http.ResponseWriter) bool { return s.watchSvcFor(w, context.Background()) == nil },
		"lease": func(w http.ResponseWriter) bool { return s.leaseSvcFor(w, context.Background()) == nil },
		"index": func(w http.ResponseWriter) bool { return s.indexSvcFor(w, context.Background()) == nil },
		"ts":    func(w http.ResponseWriter) bool { return s.tsSvcFor(w, context.Background()) == nil },
		"vector": func(w http.ResponseWriter) bool {
			return s.vectorSvcFor(w, context.Background()) == nil
		},
		"tx":    func(w http.ResponseWriter) bool { return s.txSvcFor(w, context.Background()) == nil },
		"batch": func(w http.ResponseWriter) bool { return s.batchSvcFor(w, context.Background()) == nil },
	}
	for name, resolve := range resolvers {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if !resolve(rec) {
				t.Error("expected nil service on registry error")
			}
			assertStatus(t, rec, http.StatusNotFound)
		})
	}
}

func TestKvSvcFor_InvalidTenantIDCharError(t *testing.T) {
	reg := &errServiceRegistry{err: &tenant.InvalidTenantIDCharError{TenantID: "a/b", Byte: '/'}}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithServiceRegistry(reg))
	rec := httptest.NewRecorder()
	if svc := s.kvSvcFor(rec, context.Background()); svc != nil {
		t.Error("expected nil KVService on registry error")
	}
	assertStatus(t, rec, http.StatusBadRequest)
}
