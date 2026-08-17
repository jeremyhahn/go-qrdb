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
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// StandaloneModeHint is the remedy text carried by every raft-only API
// refusal under mode "standalone".
const StandaloneModeHint = "requires mode: raft"

// WithStandaloneMode marks the server as running the raft-free standalone
// composition. The transports' per-(tenant, db) service registry is
// wrapped so the raft-only resolutions — MVCC, watch, lease, and the
// etcd-style transaction API — are refused with a typed error naming the
// remedy, while the standard-operations surface (KV, batch, index,
// timeseries, vector, and the SQL catalog services, which consume the
// MVCC-capable engine internally) resolves as usual. The caller is
// responsible for not passing the raft-only singleton services
// (WithMVCC/WithWatch/WithLease/WithLockService/WithTransaction/
// WithRebalance/WithBackupService) in this mode.
func WithStandaloneMode() ServerOption {
	return func(o *serverOptions) { o.standaloneMode = true }
}

// transportServiceRegistry returns the per-(tenant, db) registry handed to
// the protocol transports: the plain registry in raft mode, or the
// raft-only-gated wrapper in standalone mode. The SQL surfaces
// (server_sql.go, server_mysql.go, server_pgsql.go) deliberately keep
// calling buildServiceRegistry directly — the relational catalog consumes
// the MVCC/transaction servicers as internal implementation details of
// standard database operations, which standalone serves.
func (s *Server) transportServiceRegistry() service.Registry {
	reg := s.buildServiceRegistry()
	if reg == nil || !s.opts.standaloneMode {
		return reg
	}
	return &raftOnlyGatedRegistry{inner: reg}
}

// raftOnlyGatedRegistry refuses the raft-only registry resolutions under
// mode "standalone" and passes everything else through.
type raftOnlyGatedRegistry struct {
	inner service.Registry
}

func (r *raftOnlyGatedRegistry) KVService(tenant, db string) (service.KVServicer, error) {
	return r.inner.KVService(tenant, db)
}

func (r *raftOnlyGatedRegistry) MVCCService(tenant, db string) (service.MVCCKVServicer, error) {
	return nil, &rest.ServiceNotImplementedError{Service: "mvcc", Hint: StandaloneModeHint}
}

func (r *raftOnlyGatedRegistry) WatchService(tenant, db string) (service.WatchServicer, error) {
	return nil, &rest.ServiceNotImplementedError{Service: "watch", Hint: StandaloneModeHint}
}

func (r *raftOnlyGatedRegistry) LeaseService(tenant, db string) (service.LeaseServicer, error) {
	return nil, &rest.ServiceNotImplementedError{Service: "lease", Hint: StandaloneModeHint}
}

func (r *raftOnlyGatedRegistry) TransactionService(tenant, db string) (service.TransactionServicer, error) {
	return nil, &rest.ServiceNotImplementedError{Service: "transaction", Hint: StandaloneModeHint}
}

func (r *raftOnlyGatedRegistry) IndexService(tenant, db string) (service.IndexServicer, error) {
	return r.inner.IndexService(tenant, db)
}

func (r *raftOnlyGatedRegistry) TimeSeriesService(tenant, db string) (service.TimeSeriesServicer, error) {
	return r.inner.TimeSeriesService(tenant, db)
}

func (r *raftOnlyGatedRegistry) VectorService(tenant, db string) (service.VectorServicer, error) {
	return r.inner.VectorService(tenant, db)
}

func (r *raftOnlyGatedRegistry) BatchService(tenant, db string) (service.BatchServicer, error) {
	return r.inner.BatchService(tenant, db)
}

func (r *raftOnlyGatedRegistry) TableService(tenant, db string) (service.TableServicer, error) {
	return r.inner.TableService(tenant, db)
}

func (r *raftOnlyGatedRegistry) SequenceService(tenant, db string) (service.SequenceServicer, error) {
	return r.inner.SequenceService(tenant, db)
}

func (r *raftOnlyGatedRegistry) PrivilegeService(tenant, db string) (service.PrivilegeServicer, error) {
	return r.inner.PrivilegeService(tenant, db)
}

// compile-time interface check.
var _ service.Registry = (*raftOnlyGatedRegistry)(nil)
