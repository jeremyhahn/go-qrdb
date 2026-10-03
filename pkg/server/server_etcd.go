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

//go:build qrdb_etcd

package server

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/etcd"
	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func init() {
	startEtcdServerFn = startEtcd
	stopEtcdServerFn = stopEtcd
}

// startEtcd creates and starts an etcd protocol server for the named
// database.
//
// The listener is confined to exactly that database: its MVCC, transaction,
// watch and lease services are resolved through the same per-(tenant,
// database) service registry every other transport uses, for the database's
// owning tenant, so it sees the "<tenant>/<database>/" keyspace of the shared
// shard -- or the tenant's own store under volume and raft isolation -- and
// nothing else. It refuses to start when that confinement or the node's
// authentication is unavailable, rather than serve the raw store or an
// unauthenticated port.
func startEtcd(s *Server, dbName string, cfg *database.EtcdConfig) (EtcdProtocolServer, error) {
	if err := etcd.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if s.opts.tlsConfig == nil || s.opts.authMW == nil {
		return nil, &etcd.AuthRequiredError{Database: dbName}
	}
	if s.opts.dbManager == nil {
		return nil, &etcd.ScopeUnavailableError{Database: dbName, Reason: "no database catalog is configured"}
	}
	db, err := s.opts.dbManager.Get(dbName)
	if err != nil {
		return nil, err
	}
	owner := tenantid.Normalize(db.TenantID())
	// The etcd compatibility listener is raft-only (capabilities.Etcd). A
	// standalone node serves the MVCC, watch and lease families it would
	// confine to, but not this product surface; see WithStandaloneMode.
	if s.opts.standaloneMode {
		return nil, notImplemented("etcd", StandaloneModeHint)
	}
	reg := s.buildServiceRegistry()
	if reg == nil {
		return nil, &etcd.ScopeUnavailableError{Database: dbName, Reason: "no per-database service registry is configured"}
	}

	opts, err := etcdScopedServices(reg, owner, dbName)
	if err != nil {
		return nil, err
	}
	opts = append(opts,
		etcd.WithTLS(s.opts.tlsConfig),
		etcd.WithRequestAuthorizer(&etcdRequestAuthorizer{mw: s.opts.authMW, reg: reg, mgr: s.opts.dbManager, owner: owner, db: dbName, logger: s.logger, streams: s.clusterSealStreams(), nodeRefusal: s.nodeRefusal, streamRefusal: etcdStreamRefusal(reg, owner, dbName)}),
	)
	if s.adminSvc != nil {
		opts = append(opts, etcd.WithAdmin(s.adminSvc))
	}

	srv := etcd.NewServer(dbName, cfg.ListenAddr, opts...)
	if err := srv.Start(); err != nil {
		return nil, err
	}

	s.etcdServers.Store(dbName, srv)
	s.logger.Info("etcd server started", "database", dbName, "tenant", owner, "addr", srv.Addr())

	return srv, nil
}

// etcdScopedServices resolves the etcd listener's engines for (owner, db)
// through reg. An engine the database does not serve (a capability refusal)
// is left out, so the listener does not register that etcd service; any
// other resolution error -- a sealed database, an unwired isolation backend
// -- refuses the start.
func etcdScopedServices(reg service.Registry, owner, db string) ([]etcd.ServerOption, error) {
	mvcc, err := reg.MVCCService(owner, db)
	if err != nil {
		return nil, err
	}
	opts := []etcd.ServerOption{etcd.WithMVCC(mvcc)}
	if tx, err := reg.TransactionService(owner, db); err == nil {
		opts = append(opts, etcd.WithTransaction(tx))
	} else if !isCapabilityRefusal(err) {
		return nil, err
	}
	if w, err := reg.WatchService(owner, db); err == nil {
		opts = append(opts, etcd.WithWatch(w))
	} else if !isCapabilityRefusal(err) {
		return nil, err
	}
	if l, err := reg.LeaseService(owner, db); err == nil {
		opts = append(opts, etcd.WithLease(l))
	} else if !isCapabilityRefusal(err) {
		return nil, err
	}
	return opts, nil
}

// isCapabilityRefusal reports whether err is the registry's "this database
// does not serve that engine" answer.
func isCapabilityRefusal(err error) bool {
	var cue *service.CapabilityUnsupportedError
	return errors.As(err, &cue)
}

// etcdRequestAuthorizer authenticates every etcd RPC with the node's
// authenticator chain, confines it to the database's owning tenant, checks
// the RPC's permission for that tenant, and re-resolves the database so a
// sealed or dropped database is refused on the next request rather than at
// the next restart.
//
// A caller from another tenant is refused with the generic
// etcd.PermissionDeniedError: the refusal must not name the owning tenant or
// the database, which would tell a stranger what the port serves. The detail
// is logged server-side instead.
type etcdRequestAuthorizer struct {
	mw     *authmw.AuthMiddleware
	reg    service.Registry
	mgr    *database.Manager
	owner  string
	db     string
	logger *slog.Logger
	// streams, when non-nil, also ends every etcd stream when the database's
	// tenant, the database or the node barrier is sealed, or this node is cut
	// off from the seal records.
	streams *clusterseal.Streams
	// nodeRefusal, when non-nil, refuses every request and stream open while
	// the node does (Server.nodeRefusal): the etcd listener has no
	// sealed-window middleware of its own.
	nodeRefusal func() error
	// streamRefusal, when non-nil, is the database's own refusal -- its
	// tenant or itself sealed, here or by its record -- re-asked once a
	// stream has subscribed (service.StreamRefuser).
	streamRefusal func() error
}

// refuseIfNodeRefuses answers the node-wide refusal as a gRPC status.
func (a *etcdRequestAuthorizer) refuseIfNodeRefuses() error {
	if a.nodeRefusal == nil {
		return nil
	}
	if err := a.nodeRefusal(); err != nil {
		return status.Error(grpcapi.ServiceErrorCode(err), err.Error())
	}
	return nil
}

var (
	_ etcd.RequestAuthorizer = (*etcdRequestAuthorizer)(nil)
	_ etcd.StreamGuard       = (*etcdRequestAuthorizer)(nil)
)

// GuardStream implements etcd.StreamGuard: a Watch or LeaseKeepAlive stream
// ends, with the sealed refusal or the dropped database's not-found, the
// moment the listener's database stops serving.
func (a *etcdRequestAuthorizer) GuardStream(ctx context.Context) (<-chan struct{}, func() error, error) {
	dbase, err := a.mgr.Get(a.db)
	if err != nil {
		return nil, nil, status.Error(grpcapi.ServiceErrorCode(err), err.Error())
	}
	refusal := func() error {
		e := databaseUnservedError(dbase, a.owner, a.db)
		return status.Error(grpcapi.ServiceErrorCode(e), e.Error())
	}
	if a.streams == nil {
		// A stream does not open while the node refuses.
		if err := a.refuseIfNodeRefuses(); err != nil {
			return nil, nil, err
		}
		return dbase.Unserved(), refusal, nil
	}
	// Subscribe FIRST, then ask the refusals: a seal closes its gate before
	// it ends streams, so one landing before the subscription is refused
	// here and one landing after it ends the subscription
	// (clusterseal.Streams.SubscribeServing).
	sub, err := a.streams.SubscribeServing(clusterSealTenant(a.owner), a.db, a.openRefusal)
	if err != nil {
		return nil, nil, err
	}
	ended := make(chan struct{})
	// The stream ends on whichever comes first: the database stops serving,
	// or a seal ends the subscription. ctx is the stream's own, so the
	// merge ends with the stream.
	go func() {
		defer sub.Release()
		select {
		case <-dbase.Unserved():
			close(ended)
		case <-sub.Done():
			close(ended)
		case <-ctx.Done():
		}
	}()
	sealedRefusal := func() error {
		if cause := sub.Cause(); cause != nil {
			return status.Error(grpcapi.ServiceErrorCode(cause), cause.Error())
		}
		return refusal()
	}
	return ended, sealedRefusal, nil
}

// openRefusal is a stream open's refusal, as a gRPC status: the node's, then
// the database's own.
func (a *etcdRequestAuthorizer) openRefusal() error {
	if err := a.refuseIfNodeRefuses(); err != nil {
		return err
	}
	if a.streamRefusal != nil {
		if err := a.streamRefusal(); err != nil {
			return status.Error(grpcapi.ServiceErrorCode(err), err.Error())
		}
	}
	return nil
}

// AuthorizeRequest implements etcd.RequestAuthorizer.
func (a *etcdRequestAuthorizer) AuthorizeRequest(ctx context.Context, perm permission.Permission) (context.Context, error) {
	if err := a.refuseIfNodeRefuses(); err != nil {
		return nil, err
	}
	ctx = withPeerTLSState(ctx)
	ctx, identity, err := a.mw.Authenticate(ctx)
	if err != nil {
		return nil, authmw.GRPCError(err, codes.Unauthenticated)
	}
	if identity == nil {
		return nil, authmw.GRPCError(auth.ErrUnauthenticated, codes.Unauthenticated)
	}
	// The listener serves its owning tenant and no one else -- the system
	// tenant included: there is no cross-tenant database access.
	callerTenant := tenantid.Normalize(identity.TenantID)
	if !database.OwnedBy(a.owner, callerTenant) {
		if a.logger != nil {
			a.logger.Warn("etcd request refused",
				"subject", identity.Subject,
				"err", &etcd.TenantMismatchError{CallerTenant: callerTenant, OwnerTenant: a.owner, Database: a.db})
		}
		return nil, status.Error(codes.PermissionDenied, (&etcd.PermissionDeniedError{}).Error())
	}
	ctx = auth.WithIdentity(ctx, identity)
	if err := a.mw.AuthorizeTenant(ctx, a.owner, perm); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}
	if _, err := a.reg.MVCCService(a.owner, a.db); err != nil {
		return nil, status.Error(grpcapi.ServiceErrorCode(err), err.Error())
	}
	return tenant.WithTenantID(ctx, callerTenant), nil
}

// withPeerTLSState stores the gRPC peer's TLS connection state in ctx for
// the mTLS authenticator.
func withPeerTLSState(ctx context.Context) context.Context {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ctx
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return ctx
	}
	return auth.WithTLSState(ctx, &tlsInfo.State)
}

// stopEtcd stops a running etcd protocol server.
func stopEtcd(srv EtcdProtocolServer) {
	srv.Stop()
}

// etcdStreamRefusal returns reg's stream refusal for (owner, db), or nil when
// reg has none.
func etcdStreamRefusal(reg service.Registry, owner, db string) func() error {
	r, ok := reg.(service.StreamRefuser)
	if !ok {
		return nil
	}
	return func() error { return r.StreamRefusal(owner, db) }
}
