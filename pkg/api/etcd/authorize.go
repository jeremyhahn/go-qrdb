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

// streamAuthorize is the stream interceptor every etcd stream passes
// through. The authorization is made once, when the stream opens: a stream
// already open when its database is sealed or dropped, or its caller loses
// the permission, runs until it ends. That limit is documented in
// docs/etcd-compatibility/engineering.md.
func (s *Server) streamAuthorize(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	actx, err := s.authorizeRPC(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	return handler(srv, &authorizedStream{ServerStream: ss, ctx: actx})
}

// authorizedStream carries the authorized context into a stream handler.
type authorizedStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the authorized context.
func (a *authorizedStream) Context() context.Context { return a.ctx }
