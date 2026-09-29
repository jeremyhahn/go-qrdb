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

package transport

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Database scoping wire constants. These mirror the server-side constants so
// the SDK and server agree on how the target database is conveyed.
const (
	// DatabaseHeader is the REST HTTP header that selects the target database
	// within the authenticated tenant's keyspace.
	DatabaseHeader = "X-QRDB-Database"

	// DatabaseMetadataKey is the gRPC/Unix outgoing metadata key that selects
	// the target database. gRPC lower-cases metadata keys.
	DatabaseMetadataKey = "x-qrdb-database"

	// DefaultDatabaseScope is the sentinel name for the server's implicit
	// bootstrap database. Scoping to it is identical to being unscoped: the
	// server uses this database whenever no scope is conveyed, so no header or
	// metadata is sent for either the empty string or this name.
	DefaultDatabaseScope = "system"
)

// NormalizeDatabaseScope maps the well-known default database name to the empty
// string so that scoping to "system" is treated as unscoped (no wire header /
// metadata is sent). All other names are returned unchanged.
func NormalizeDatabaseScope(name string) string {
	if name == DefaultDatabaseScope {
		return ""
	}
	return name
}

// DatabaseScoper is implemented by transports that support per-database
// request scoping. WithDatabase returns a transport that conveys the given
// database on its data-plane calls while sharing the underlying connection.
// An empty database (or "system") yields an unscoped transport. A transport
// that cannot confine its data plane to the named database returns a
// *DatabaseScopeUnsupportedError rather than a transport that would silently
// serve the default database.
type DatabaseScoper interface {
	WithDatabase(database string) (Transport, error)
}

// DatabaseUnaryClientInterceptor returns a gRPC unary client interceptor that
// appends the x-qrdb-database metadata to every RPC. The server only consults
// it for data-plane handlers; management and admin RPCs ignore it, so universal
// injection is safe. Shared by the gRPC and Unix SDK transports.
func DatabaseUnaryClientInterceptor(db string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, DatabaseMetadataKey, db)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// DatabaseStreamClientInterceptor returns a gRPC stream client interceptor that
// appends the x-qrdb-database metadata to every streaming RPC (e.g. Watch).
func DatabaseStreamClientInterceptor(db string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx = metadata.AppendToOutgoingContext(ctx, DatabaseMetadataKey, db)
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// dataPlanePrefixes are the REST path prefixes for data-plane endpoints (KV,
// MVCC, transaction, batch, watch, lease, lock, secondary index, time-series,
// and vector). Only requests to these paths carry the database scope header;
// management, admin, and system endpoints are never database-scoped via the
// header because they are tenant- or cluster-global (and collection paths embed
// the database in the URL).
var dataPlanePrefixes = []string{
	"/kv",
	"/tx",
	"/batch",
	"/watch",
	"/lease",
	"/lock",
	"/index",
	"/ts",
	"/vector",
}

// IsDataPlanePath reports whether a REST request path targets a data-plane
// endpoint and therefore should carry the database scope header. A prefix
// matches only on a path-segment boundary (exact, "/" separator, or "?" query
// start) so that, e.g., "/kv" and "/kv/scan" match while an unrelated path
// beginning with the same letters does not.
func IsDataPlanePath(path string) bool {
	for _, p := range dataPlanePrefixes {
		if path == p || strings.HasPrefix(path, p+"/") || strings.HasPrefix(path, p+"?") {
			return true
		}
	}
	return false
}
