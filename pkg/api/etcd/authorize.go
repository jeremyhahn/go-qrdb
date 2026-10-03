//go:build qrdb_etcd

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

package etcd

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// RequestAuthorizer authenticates and authorizes one etcd RPC before its
// handler runs. It receives the qrdb permission the RPC requires and returns
// the context the handler runs with -- carrying the caller's identity and
// tenant -- or an error carrying a gRPC status. The etcd Auth service is not
// implemented, so this is the listener's only authentication, and Start
// refuses to serve without one.
type RequestAuthorizer interface {
	AuthorizeRequest(ctx context.Context, perm permission.Permission) (context.Context, error)
}

// WithRequestAuthorizer configures the authorizer every RPC passes through.
func WithRequestAuthorizer(a RequestAuthorizer) ServerOption {
	return func(o *serverOptions) { o.authorizer = a }
}

// methodPermissions is the qrdb permission each served etcd RPC requires. It
// is default-deny: an RPC with no row -- the Auth service stub, membership
// changes, snapshots, defragmentation and the other maintenance verbs -- is
// refused before its handler runs, so a verb added to the etcd API cannot
// become reachable without a decision here.
var methodPermissions = map[string]permission.Permission{
	"/etcdserverpb.KV/Range":              permission.KVRange,
	"/etcdserverpb.KV/Put":                permission.KVPut,
	"/etcdserverpb.KV/DeleteRange":        permission.KVDelete,
	"/etcdserverpb.KV/Txn":                permission.TransactionExecute,
	"/etcdserverpb.KV/Compact":            permission.MVCCCompact,
	"/etcdserverpb.Watch/Watch":           permission.WatchWatchPrefix,
	"/etcdserverpb.Lease/LeaseGrant":      permission.LeaseGrant,
	"/etcdserverpb.Lease/LeaseRevoke":     permission.LeaseRevoke,
	"/etcdserverpb.Lease/LeaseKeepAlive":  permission.LeaseKeepAlive,
	"/etcdserverpb.Lease/LeaseTimeToLive": permission.LeaseTTL,
	"/etcdserverpb.Lease/LeaseLeases":     permission.LeaseList,
	"/etcdserverpb.Cluster/MemberList":    permission.AdminPeers,
	"/etcdserverpb.Maintenance/Status":    permission.AdminStatus,
	"/etcdserverpb.Maintenance/Alarm":     permission.AdminStatus,
}

// MethodPermission returns the qrdb permission the etcd RPC fullMethod
// requires, and false for an RPC the listener refuses outright.
func MethodPermission(fullMethod string) (permission.Permission, bool) {
	perm, ok := methodPermissions[fullMethod]
	return perm, ok
}

// authorizeRPC runs the authorizer for fullMethod and returns the context the
// handler runs with.
func (s *Server) authorizeRPC(ctx context.Context, fullMethod string) (context.Context, error) {
	perm, ok := MethodPermission(fullMethod)
	if !ok {
		return nil, status.Error(codes.PermissionDenied, (&MethodNotAllowedError{Method: fullMethod}).Error())
	}
	actx, err := s.opts.authorizer.AuthorizeRequest(ctx, perm)
	if err != nil {
		if _, isStatus := status.FromError(err); isStatus {
			return nil, err
		}
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	return actx, nil
}

// unaryAuthorize is the unary interceptor every etcd RPC passes through.
func (s *Server) unaryAuthorize(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	actx, err := s.authorizeRPC(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	return handler(actx, req)
}

// StreamGuard is implemented by a RequestAuthorizer whose database can stop
// serving while a stream is open. GuardStream returns a channel that closes
// when the database stops serving -- it is sealed or dropped -- and the error
// the stream then ends with, carrying its gRPC status.
type StreamGuard interface {
	GuardStream(ctx context.Context) (unserved <-chan struct{}, refusal func() error, err error)
}

// streamAuthorize is the stream interceptor every etcd stream passes
// through. The authorization is made when the stream opens; when the
// authorizer is a StreamGuard, the stream also ends -- with the database's
// typed refusal -- the moment its database is sealed or dropped, instead of
// running on over a database every new request is refused. A change to the
// caller's roles takes effect on its next stream, not on one already open.
func (s *Server) streamAuthorize(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	actx, err := s.authorizeRPC(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	stream := &authorizedStream{ServerStream: ss, ctx: actx}
	if guard, ok := s.opts.authorizer.(StreamGuard); ok {
		unserved, refusal, err := guard.GuardStream(actx)
		if err != nil {
			return err
		}
		stream.unserved, stream.refusal = unserved, refusal
	}
	return handler(srv, stream)
}

// authorizedStream carries the authorized context into a stream handler and,
// when guarded, ends the stream once its database stops serving.
type authorizedStream struct {
	grpc.ServerStream
	ctx      context.Context
	unserved <-chan struct{}
	refusal  func() error
}

// Context returns the authorized context.
func (a *authorizedStream) Context() context.Context { return a.ctx }

// RecvMsg receives the next client message, or answers the database's
// refusal as soon as the database stops serving -- a client that sends
// nothing more must not hold a stream open on a sealed database. The pending
// receive is released when the handler returns the refusal and gRPC ends the
// stream.
func (a *authorizedStream) RecvMsg(m any) error {
	if a.unserved == nil {
		return a.ServerStream.RecvMsg(m)
	}
	done := make(chan error, 1)
	go func() { done <- a.ServerStream.RecvMsg(m) }()
	select {
	case err := <-done:
		return err
	case <-a.unserved:
		return a.refusal()
	}
}

// SendMsg sends m unless the database has stopped serving.
func (a *authorizedStream) SendMsg(m any) error {
	if a.unserved != nil {
		select {
		case <-a.unserved:
			return a.refusal()
		default:
		}
	}
	return a.ServerStream.SendMsg(m)
}
