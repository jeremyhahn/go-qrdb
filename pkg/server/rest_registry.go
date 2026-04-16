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

package server

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// restServiceRegistry is the concrete implementation of rest.ServiceRegistry.
// It bridges the per-database DatabaseServiceRegistry with the per-tenant
// TenantServicer and BarrierRegistry to enforce (tenant, db) request scoping
// with per-tenant keyspace isolation.
//
// Tenant resolution order:
//  1. Look up the tenant via tenantSvc.GetTenant. Unknown tenant →
//     TenantNotFoundError (404). When no tenantSvc is configured, the lookup
//     is skipped and any tenant name is accepted (backward-compatible).
//  2. When a BarrierRegistry is configured and the tenant uses IsolationBarrier,
//     validate that the tenant's barrier is not sealed.
//  3. The database is resolved from the DatabaseServiceRegistry. When the
//     database is not found, DatabaseNotFoundError is returned.
//  4. All non-default tenants receive a namespace-prefixed KVService so that
//     their keys are physically separated under the shared Raft state machine.
//
// Both IsolationBarrier and IsolationPrefix modes use namespace prefixing for
// keyspace separation. The difference is that IsolationBarrier additionally
// validates the per-tenant cryptographic barrier (when the seal subsystem is
// configured). The IsolationType name reflects what the admin is configuring
// from their perspective.
//
// restServiceRegistry is safe for concurrent use.
type restServiceRegistry struct {
	dbReg         *DatabaseServiceRegistry
	barrierReg    *seal.BarrierRegistry
	tenantSvc     tenant.TenantServicer
	defaultTenant string
}

// NewRESTServiceRegistry creates a rest.ServiceRegistry that resolves
// per-(tenant, db) services from the given database registry, using the
// optional barrier registry for sealed-state enforcement and the optional
// tenant service for IsolationType lookup and unknown-tenant rejection.
//
// When barrierReg is nil, barrier checks are skipped.
// When tenantSvc is nil, tenant lookup and unknown-tenant rejection are skipped
// (backward-compatible with deployments that do not use the tenant service).
// defaultTenant is the tenant name that bypasses namespace prefixing (typically
// "default") so that single-tenant deployments continue to work unchanged.
func NewRESTServiceRegistry(
	dbReg *DatabaseServiceRegistry,
	barrierReg *seal.BarrierRegistry,
	defaultTenant string,
	opts ...RESTRegistryOption,
) rest.ServiceRegistry {
	r := &restServiceRegistry{
		dbReg:         dbReg,
		barrierReg:    barrierReg,
		defaultTenant: defaultTenant,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RESTRegistryOption configures optional restServiceRegistry dependencies.
type RESTRegistryOption func(*restServiceRegistry)

// WithTenantServicer sets the tenant service used to look up tenant metadata
// (including IsolationType) and reject requests for unknown tenants.
func WithTenantServicer(svc tenant.TenantServicer) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.tenantSvc = svc
	}
}

// resolve validates the (tenant, db) pair and returns the DatabaseServices
// wrapped with per-tenant namespace isolation. For non-default tenants, the
// KV service is wrapped with a prefix so that each tenant's keys are
// logically separated under the shared Raft state machine.
func (r *restServiceRegistry) resolve(tenantID, db string) (*DatabaseServices, error) {
	isolation, err := r.resolveTenant(tenantID)
	if err != nil {
		return nil, err
	}

	svcs := r.dbReg.GetOrDefault(db)
	if svcs == nil {
		return nil, &rest.DatabaseNotFoundError{TenantID: tenantID, Database: db}
	}

	// The default tenant uses the shared keyspace without any prefix. All
	// other tenants receive a namespaced copy of DatabaseServices that
	// transparently prepends "{tenantID}/" to every key.
	if tenantID == r.defaultTenant {
		return svcs, nil
	}

	return r.applyNamespace(svcs, tenantID, isolation), nil
}

// resolveTenant looks up the tenant metadata (when a tenant service is
// configured) and validates the barrier state. It returns the effective
// IsolationType for the tenant.
func (r *restServiceRegistry) resolveTenant(tenantID string) (tenant.IsolationType, error) {
	if r.tenantSvc == nil {
		// No tenant service: skip lookup, behave as before.
		return tenant.IsolationBarrier, r.checkTenantBarrier(tenantID)
	}

	info, err := r.tenantSvc.GetTenant(context.Background(), tenantID)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			return "", &rest.TenantNotFoundError{TenantID: tenantID}
		}
		return "", &rest.TenantNotFoundError{TenantID: tenantID}
	}

	isolation := info.EffectiveIsolationType()

	// For barrier mode, also validate the per-tenant barrier state.
	if isolation == tenant.IsolationBarrier {
		if err := r.checkTenantBarrier(tenantID); err != nil {
			return "", err
		}
	}

	return isolation, nil
}

// checkTenantBarrier validates the tenant against the barrier registry when
// applicable. It is a no-op for the default tenant or when no registry is
// configured.
func (r *restServiceRegistry) checkTenantBarrier(tenantID string) error {
	if r.barrierReg == nil || tenantID == r.defaultTenant {
		return nil
	}
	tb, err := r.barrierReg.GetTenant(tenantID)
	if err != nil {
		if errors.Is(err, seal.ErrTenantNotFound) || errors.Is(err, seal.ErrEmptyTenantID) {
			return &rest.TenantNotFoundError{TenantID: tenantID}
		}
		return &rest.TenantNotFoundError{TenantID: tenantID}
	}
	if tb.IsSealed() {
		return &rest.BarrierSealedError{TenantID: tenantID}
	}
	return nil
}

// applyNamespace returns a copy of svcs with the KV service wrapped in a
// per-tenant namespace prefix. Other services (MVCC, Watch, etc.) are returned
// unchanged for now; they operate on top of the KV service via Raft proposals
// and will be migrated to namespace-aware wrappers in a future phase.
func (r *restServiceRegistry) applyNamespace(svcs *DatabaseServices, tenantID string, _ tenant.IsolationType) *DatabaseServices {
	if svcs.KV == nil {
		return svcs
	}
	copy := *svcs
	copy.KV = newNamespacedKVService(svcs.KV, tenantID)
	return &copy
}

// KVService returns the KVServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) KVService(tenantID, db string) (service.KVServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.KV == nil {
		return nil, &rest.ServiceNotImplementedError{Service: "kv"}
	}
	return svcs.KV, nil
}

// MVCCService returns the MVCCKVServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) MVCCService(tenantID, db string) (service.MVCCKVServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.MVCC == nil {
		return nil, &rest.ServiceNotImplementedError{Service: "mvcc"}
	}
	return svcs.MVCC, nil
}

// WatchService returns the WatchServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) WatchService(tenantID, db string) (service.WatchServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Watch == nil {
		return nil, &rest.ServiceNotImplementedError{Service: "watch"}
	}
	return svcs.Watch, nil
}

// LeaseService returns the LeaseServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) LeaseService(tenantID, db string) (service.LeaseServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Lease == nil {
		return nil, &rest.ServiceNotImplementedError{Service: "lease"}
	}
	return svcs.Lease, nil
}

// IndexService returns the IndexServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) IndexService(tenantID, db string) (service.IndexServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Index == nil {
		return nil, &rest.ServiceNotImplementedError{Service: "index"}
	}
	return svcs.Index, nil
}

// TimeSeriesService returns the TimeSeriesServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) TimeSeriesService(tenantID, db string) (service.TimeSeriesServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.TimeSeries == nil {
		return nil, &rest.ServiceNotImplementedError{Service: "timeseries"}
	}
	return svcs.TimeSeries, nil
}

// VectorService returns the VectorServicer for the given (tenant, db) scope.
// VectorServicer is not yet tracked per-database in DatabaseServices;
// this method returns ServiceNotImplementedError until it is added.
func (r *restServiceRegistry) VectorService(tenantID, db string) (service.VectorServicer, error) {
	if _, err := r.resolve(tenantID, db); err != nil {
		return nil, err
	}
	return nil, &rest.ServiceNotImplementedError{Service: "vector"}
}

// TransactionService returns the TransactionServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) TransactionService(tenantID, db string) (service.TransactionServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Transaction == nil {
		return nil, &rest.ServiceNotImplementedError{Service: "transaction"}
	}
	return svcs.Transaction, nil
}

// BatchService returns the BatchServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) BatchService(tenantID, db string) (service.BatchServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Batch == nil {
		return nil, &rest.ServiceNotImplementedError{Service: "batch"}
	}
	return svcs.Batch, nil
}
