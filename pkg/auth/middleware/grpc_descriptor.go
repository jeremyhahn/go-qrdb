// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	qrdbv1 "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// grpcPermEntry is a memoized descriptor-lookup result for one gRPC full
// method name. found is false when the method has no (qrdb.v1.permission)
// option (or does not exist), which the interceptors treat as default-deny.
type grpcPermEntry struct {
	perm  permission.Permission
	found bool
}

// grpcPermCache memoizes LookupGRPCFromDescriptor results. The gRPC server
// only invokes interceptors for methods registered on it, so the cache is
// bounded by the registered method set (plus the health infrastructure
// methods) and lock-free reads keep the per-RPC hot path cheap.
var grpcPermCache sync.Map // string -> grpcPermEntry

// LookupGRPCFromDescriptor returns the permission required for a gRPC full
// method name (e.g. "/qrdb.v1.QRDBService/Put") by resolving the method
// descriptor in the global protobuf registry and reading its
// (qrdb.v1.permission) method option — the single, proto-declared source of
// truth for gRPC authorization. Returns an empty Permission and false when
// the method does not exist or carries no permission option; callers treat
// that as default-deny. The sentinel value "identity:gated" converts to
// permission.IdentityGated, preserving the handler-layer authorization
// short-circuit in authorize(). The sentinel value "identity:public"
// converts to permission.Public, documenting a method the interceptors
// already bypass entirely via RegisterPublicGRPCMethods before this lookup
// ever runs.
func LookupGRPCFromDescriptor(fullMethod string) (permission.Permission, bool) {
	if v, ok := grpcPermCache.Load(fullMethod); ok {
		e := v.(grpcPermEntry) //nolint:errcheck // type assertion on a value this container only ever holds
		return e.perm, e.found
	}
	e := resolveGRPCPermission(fullMethod)
	grpcPermCache.Store(fullMethod, e)
	return e.perm, e.found
}

// resolveGRPCPermission performs the uncached descriptor resolution for
// LookupGRPCFromDescriptor. The gRPC transport identifies methods as
// "/package.Service/Method" while the protobuf registry keys descriptors by
// full name "package.Service.Method", so the leading slash is stripped and
// the separator converted before lookup.
func resolveGRPCPermission(fullMethod string) grpcPermEntry {
	name := strings.ReplaceAll(strings.TrimPrefix(fullMethod, "/"), "/", ".")
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		return grpcPermEntry{}
	}
	md, ok := desc.(protoreflect.MethodDescriptor)
	if !ok {
		return grpcPermEntry{}
	}
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return grpcPermEntry{}
	}
	if !proto.HasExtension(opts, qrdbv1.E_Permission) {
		return grpcPermEntry{}
	}
	value, ok := proto.GetExtension(opts, qrdbv1.E_Permission).(string)
	if !ok || value == "" {
		return grpcPermEntry{}
	}
	return grpcPermEntry{perm: permission.Permission(value), found: true}
}
