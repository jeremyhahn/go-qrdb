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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// StandaloneModeHint is the remedy text carried by every raft-only API
// refusal under mode "standalone".
const StandaloneModeHint = "requires mode: raft"

// notImplemented is the refusal a registry resolution answers when this node
// cannot serve the named service — because it was composed without it, or
// because the mode gates it (hint names the remedy, e.g. StandaloneModeHint).
//
// It is a SERVICE-layer error, deliberately. This registry is handed to every
// transport, and the refusal used to be *rest.ServiceNotImplementedError — a
// type only the REST transport classified, so REST answered 501 while gRPC,
// unix, QUIC and MCP fell through to their unclassified default and told a
// caller its server had BROKEN for a condition that is a deployment mode
// (go-qrdb#473). service.CapabilityUnsupportedError is the vocabulary every
// transport's classifier already maps to "this node cannot do that": 501,
// codes.Unimplemented, wire.StatusUnimplemented, CodeUnimplemented — and the
// SDK rebuilds transport.CapabilityUnsupportedError from each. Returning it
// here fixes all five transports at once, and removes the server package's
// reach into a transport's error vocabulary.
func notImplemented(svc, hint string) error {
	msg := svc + ": not implemented on this server"
	if hint != "" {
		msg += ": " + hint
	}
	return &service.CapabilityUnsupportedError{Message: msg}
}

// WithStandaloneMode marks the server as running the raft-free standalone
// composition. The transports' per-(tenant, db) service registry is
// wrapped so the raft-only resolutions — MVCC, watch and lease — are
// refused with a typed error naming the remedy, while the
// standard-operations surface (KV, batch, index, timeseries, vector,
// collections, the transaction/CAS family, and the SQL catalog services,
// which consume the MVCC-capable engine internally) resolves as usual. The caller is
// responsible for not passing the raft-only singleton services
// (WithMVCC/WithWatch/WithLease/WithLockService/WithRebalance/
// WithBackupService) in this mode.
//
// Transactions are NOT gated (owner ruling 2026-08-23, go-qrdb#62): a
// standalone node is engine-direct and single-process, so a
// compare-and-swap there is already linearizable without consensus.
// Gating it here while the SDK's in-process transport serves it would put
// the same contradiction back that #62 found, one seam over.
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
	return nil, notImplemented("mvcc", StandaloneModeHint)
}

func (r *raftOnlyGatedRegistry) WatchService(tenant, db string) (service.WatchServicer, error) {
	return nil, notImplemented("watch", StandaloneModeHint)
}

func (r *raftOnlyGatedRegistry) LeaseService(tenant, db string) (service.LeaseServicer, error) {
	return nil, notImplemented("lease", StandaloneModeHint)
}

// TransactionService passes through: compare-and-swap is a standard
// database operation that a single-process engine-direct node serves
// correctly (owner ruling 2026-08-23, go-qrdb#62).
func (r *raftOnlyGatedRegistry) TransactionService(tenant, db string) (service.TransactionServicer, error) {
	return r.inner.TransactionService(tenant, db)
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

// CollectionService passes through: a collection is a document container over
// the KV engine, which standalone serves. It is listed explicitly rather than
// left to a default so that adding a registry resolution cannot silently
// acquire or lose a standalone gate.
func (r *raftOnlyGatedRegistry) CollectionService(tenant, db string) (service.CollectionServicer, error) {
	return r.inner.CollectionService(tenant, db)
}

// LockService is gated: a distributed lock is composed from the lease, watch
// and MVCC resolutions this wrapper already refuses, so serving it under mode
// "standalone" would hand back a lock with no TTL expiry and no blocking
// acquisition. It refuses with the same remedy text rather than failing later
// and less legibly inside the composed servicer.
func (r *raftOnlyGatedRegistry) LockService(tenant, db string) (service.LockServicer, error) {
	return nil, notImplemented("lock", StandaloneModeHint)
}

// compile-time interface check.
var _ service.Registry = (*raftOnlyGatedRegistry)(nil)
