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
	"google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// RegisterOption configures optional service dependencies for RegisterService.
type RegisterOption func(*qrdbServiceServer)

// WithRegisterKVService attaches a KV service for the registered gRPC service.
// This is an alternative to passing kvSvc as a positional parameter.
func WithRegisterKVService(svc service.KVServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.kvSvc = svc }
}

// WithRegisterAdminService attaches an admin service for the registered gRPC
// service. This is an alternative to passing adminSvc as a positional parameter.
func WithRegisterAdminService(svc service.AdminServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.adminSvc = svc }
}

// WithRegisterMVCC attaches an MVCC service for the registered gRPC service.
func WithRegisterMVCC(svc service.MVCCKVServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.mvccSvc = svc }
}

// WithRegisterTransaction attaches a transaction service for the registered gRPC service.
func WithRegisterTransaction(svc service.TransactionServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.txSvc = svc }
}

// WithRegisterBatch attaches a batch service for the registered gRPC service.
func WithRegisterBatch(svc service.BatchServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.batchSvc = svc }
}

// WithRegisterWatch attaches a watch service for the registered gRPC service.
func WithRegisterWatch(svc service.WatchServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.watchSvc = svc }
}

// WithRegisterLease attaches a lease service for the registered gRPC service.
func WithRegisterLease(svc lease.LeaseServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.leaseSvc = svc }
}

// WithRegisterIndex attaches an index service for the registered gRPC service.
func WithRegisterIndex(svc index.IndexServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.indexSvc = svc }
}

// WithRegisterTimeSeries attaches a time-series service for the registered gRPC service.
func WithRegisterTimeSeries(svc timeseries.TimeSeriesServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.tsSvc = svc }
}

// WithRegisterVector attaches a vector service for the registered gRPC service.
func WithRegisterVector(svc service.VectorServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.vectorSvc = svc }
}

// WithRegisterUserService attaches a user management service for the registered gRPC service.
func WithRegisterUserService(svc user.UserServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.userSvc = svc }
}

// WithRegisterUserAuthFactorService attaches the user-side auth factor
// service. Unlike the other options, the service is not dispatched by a
// QRDBService RPC: RegisterService registers it as the separate
// qrdb.v1.UserAuthFactorService on the same *grpc.Server (see
// RegisterUserAuthFactorService), so composers (e.g. go-quicbaas) can expose
// the user-login factor surface with a single RegisterService call.
func WithRegisterUserAuthFactorService(svc service.UserAuthFactorServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.userAuthFactorSvc = svc }
}

// WithRegisterRoleService attaches a role management service for the registered gRPC service.
func WithRegisterRoleService(svc role.RoleServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.roleSvc = svc }
}

// WithRegisterEmbedding attaches an embedding service for the registered gRPC service.
func WithRegisterEmbedding(svc service.EmbeddingServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.embeddingSvc = svc }
}

// WithRegisterRAGService attaches a RAG service for the registered gRPC service.
func WithRegisterRAGService(svc service.RAGServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.ragSvc = svc }
}

// WithRegisterDatabaseService attaches a database management service for the registered gRPC service.
func WithRegisterDatabaseService(svc service.DatabaseServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.databaseSvc = svc }
}

// WithRegisterBarrierService attaches a barrier management service for the registered gRPC service.
func WithRegisterBarrierService(svc service.BarrierServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.barrierSvc = svc }
}

// WithRegisterEncryptionMigrator attaches the enable-encryption migrator for the registered gRPC service.
func WithRegisterEncryptionMigrator(m service.EncryptionMigrator) RegisterOption {
	return func(s *qrdbServiceServer) { s.encMigrator = m }
}

// WithRegisterPINBackend attaches the canonical pin.PINBackend used for
// the SO PIN / User PIN compliance-gating layer on barrier administrative
// operations.
//
// The backend is decorated with the SO PIN lockout (pin.NewLockoutBackend,
// idempotent: a backend the composer already decorated keeps its one set of
// counters), so a raw backend handed straight to this transport is still
// throttled.
func WithRegisterPINBackend(backend pin.PINBackend) RegisterOption {
	backend = pin.NewLockoutBackend(backend)
	return func(s *qrdbServiceServer) { s.pinBackend = backend }
}

// WithRegisterCeremonyService attaches a ceremony management service for the registered gRPC service.
func WithRegisterCeremonyService(svc service.CeremonyServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.ceremonySvc = svc }
}

// WithRegisterCeremonyCA attaches the node's CeremonyCA for the registered
// gRPC service, so the shared CeremonyInitialize handler can issue the TLS
// and SO certificates. This is how the Unix domain socket transport (which
// shares this package's QRDBServiceServer implementation via RegisterService)
// supplies the same CA as the TCP gRPC server.
func WithRegisterCeremonyCA(ca ceremony.CeremonyCA) RegisterOption {
	return func(s *qrdbServiceServer) { s.ceremonyCA = ca }
}

// WithRegisterCredentialService attaches a credential management service for the registered gRPC service.
func WithRegisterCredentialService(svc service.CredentialServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.credentialSvc = svc }
}

// WithRegisterDEKService attaches the tenant DEK keyring service for the registered gRPC service.
func WithRegisterDEKService(svc service.DEKServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.dekSvc = svc }
}

// WithRegisterCertProvider attaches a certificate provider for the registered gRPC service.
func WithRegisterCertProvider(svc cert.CertProvider) RegisterOption {
	return func(s *qrdbServiceServer) { s.certProv = svc }
}

// WithRegisterCertEnrollment attaches the certificate issue/enrol and
// trust-anchor service for the registered gRPC service.
func WithRegisterCertEnrollment(svc service.CertEnrollmentServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.certEnroll = svc }
}

// WithRegisterTenantOnboarding attaches the tenant-create service for the
// registered gRPC service.
func WithRegisterTenantOnboarding(svc service.TenantOnboardingServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.onboarding = svc }
}

// WithRegisterAuthMiddleware attaches the auth middleware the registered gRPC
// service needs for its HANDLER-level tenancy gates — the certs surface, whose
// target tenant is in the request rather than in the caller's identity. It is
// the same instance the interceptors use; a transport that omits it gets the
// no-auth posture, in which the gates are no-ops.
func WithRegisterAuthMiddleware(mw *authmw.AuthMiddleware) RegisterOption {
	return func(s *qrdbServiceServer) { s.authMW = mw }
}

// WithRegisterCollectionService attaches a collection service for the registered gRPC service.
func WithRegisterCollectionService(svc service.CollectionServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.collectionSvc = svc }
}

// WithRegisterLockService attaches a distributed lock service for the registered gRPC service.
//
// This is the REGISTRY-LESS fallback, not the primary path. When a
// service.Registry is configured the lock servicer is resolved per
// (tenant, database) and this singleton is never consulted: a lock key
// carries no tenant segment, so a process-wide servicer built over the raw
// engines gives every tenant the same lock (go-qrdb#455).
func WithRegisterLockService(svc service.LockServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.lockSvc = svc }
}

// WithRegisterTenantService attaches a tenant management service for the registered gRPC service.
func WithRegisterTenantService(svc tenant.TenantServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.tenantSvc = svc }
}

// WithRegisterVolumeService attaches the volume pool management surface
// (docs/volume-pools/api.md) for the registered gRPC service.
func WithRegisterVolumeService(svc volume.Service) RegisterOption {
	return func(s *qrdbServiceServer) { s.volumeSvc = svc }
}

// WithRegisterBackupService attaches a backup management service for the registered gRPC service.
func WithRegisterBackupService(svc service.BackupServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.backupSvc = svc }
}

// WithRegisterRebalanceService attaches a cluster rebalance service for the registered gRPC service.
func WithRegisterRebalanceService(svc service.RebalanceServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.rebalanceSvc = svc }
}

// WithRegisterSQLService attaches a SQL query service for the registered gRPC service.
func WithRegisterSQLService(svc service.SQLServicer) RegisterOption {
	return func(s *qrdbServiceServer) { s.sqlSvc = svc }
}

// WithRegisterServiceRegistry attaches a per-(tenant, database) service
// registry for the registered gRPC service. When set, data-plane RPCs resolve
// their servicer per scope; when nil the singleton service fields are used.
// The Unix socket transport uses this to share the gRPC service implementation
// with full database scoping.
func WithRegisterServiceRegistry(reg service.Registry) RegisterOption {
	return func(s *qrdbServiceServer) { s.registry = reg }
}

// RegisterService registers the QRDBServiceServer on the given gRPC server
// using the provided KV and admin service implementations. kvSvc and adminSvc
// may be nil when only non-KV services (barrier, cert, auth) are needed;
// their RPC handlers return codes.Unimplemented when the service is nil.
// This allows other server transports (e.g., Unix domain sockets) to reuse
// the same service implementation without duplicating the protobuf glue code.
func RegisterService(srv *grpc.Server, kvSvc service.KVServicer, adminSvc service.AdminServicer, opts ...RegisterOption) {
	svc := &qrdbServiceServer{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
	}
	for _, opt := range opts {
		opt(svc)
	}
	pb.RegisterQRDBServiceServer(srv, svc)

	// The user-side auth factor surface is a separate proto service; register
	// it alongside QRDBService when supplied. RegisterUserAuthFactorService
	// no-ops on a nil servicer.
	RegisterUserAuthFactorService(srv, svc.userAuthFactorSvc, svc.userSvc)
}
