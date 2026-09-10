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

package quic

import (
	"context"
	"log/slog"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// registerModules projects each registered capability module's gRPC
// service(s) onto the QUIC frame dispatch table via its RegisterQUIC hook.
// buildHandlers calls this after every built-in handler is registered: a
// module's frame key is always a full gRPC method name
// ("/package.Service/Method"), which can never collide with a built-in's
// dot-separated key ("kv.Put") -- the two key shapes are disjoint by
// construction -- while a module-vs-module duplicate still fails
// construction loudly through the same s.handle -> methodPerms registry the
// built-ins use.
func (s *Server) registerModules() {
	for _, m := range s.modules {
		m.RegisterQUIC(&moduleGRPCRegistrar{s: s, module: m})
	}
}

// moduleGRPCRegistrar is the grpc.ServiceRegistrar the QUIC server hands to
// one module's ServiceModule.RegisterQUIC. It projects every unary method
// the module registers onto the native QUIC frame protocol: see
// RegisterService. module is the registering module itself, consulted only
// to check apimodule.GRPCPublicMethodLister for the public-method carve-out
// RegisterService documents.
type moduleGRPCRegistrar struct {
	s      *Server
	module apimodule.ServiceModule
}

// RegisterService implements grpc.ServiceRegistrar. For each unary method in
// desc, it registers a frame handler keyed by the method's full gRPC name
// ("/" + desc.ServiceName + "/" + MethodName) -- the identical string
// grpc.UnaryServerInfo.FullMethod carries on the gRPC and unix transports --
// and resolves the method's required permission from the SAME
// (qrdb.v1.permission) proto descriptor option RegisterGRPC's methods
// authorize against, so a module declares its permission exactly once. A
// method whose descriptor carries no permission option is registered with an
// empty permission, which s.handle's underlying registry rejects by
// panicking at construction -- the QUIC transport's default-deny startup
// assertion, symmetric with the REST route walk and the gRPC/unix
// interceptor's runtime denial of an undeclared method.
//
// The one exception is a method the module also declares through
// apimodule.GRPCPublicMethodLister: on gRPC and unix that bypasses BOTH
// authentication and authorization, but the native QUIC transport has no
// unauthenticated-method path at all -- every frame requires a verified mTLS
// client certificate (see Server.authenticate) with no carve-out even for
// admin.Health. A public method with no permission option is therefore not a
// construction bug; it is simply not reachable over QUIC, so it is skipped
// (logged) rather than failing the default-deny assertion. A public method
// that DOES carry a permission option is projected normally -- the public
// declaration only ever widens gRPC/unix reachability, it never narrows
// QUIC's.
//
// Streaming methods (desc.Streams) are NOT projected: the native QUIC frame
// protocol has no generic streaming path (its one streaming operation, KV
// Watch, is hand-built, not derived from a grpc.ServiceDesc), so a module's
// streaming RPCs stay unreachable over QUIC. This is logged, not fatal --
// the module's unary methods still deserve to be reachable.
func (r *moduleGRPCRegistrar) RegisterService(desc *grpc.ServiceDesc, impl any) {
	if len(desc.Streams) > 0 {
		skipped := make([]string, len(desc.Streams))
		for i, sd := range desc.Streams {
			skipped[i] = "/" + desc.ServiceName + "/" + sd.StreamName
		}
		slog.Warn("quic: module streaming methods are not projected onto the native QUIC frame protocol",
			"service", desc.ServiceName, "methods", skipped)
	}

	for _, md := range desc.Methods {
		fullMethod := "/" + desc.ServiceName + "/" + md.MethodName
		perm, ok := authmw.LookupGRPCFromDescriptor(fullMethod)
		if !ok && r.declaredPublic(fullMethod) {
			slog.Warn("quic: module method is declared public but QUIC has no unauthenticated-method path; it is not reachable over QUIC",
				"method", fullMethod)
			continue
		}
		r.s.handle(fullMethod, perm, projectGRPCMethod(fullMethod, impl, md))
	}
}

// declaredPublic reports whether the registering module declares fullMethod
// through the optional apimodule.GRPCPublicMethodLister extension.
func (r *moduleGRPCRegistrar) declaredPublic(fullMethod string) bool {
	lister, ok := r.module.(apimodule.GRPCPublicMethodLister)
	if !ok {
		return false
	}
	return slices.Contains(lister.PublicGRPCMethods(), fullMethod)
}

// projectGRPCMethod adapts one grpc.MethodDesc into a QUIC methodHandler: it
// decodes the frame payload into the request message the generated Handler
// allocates, invokes the module's own implementation with no interceptor
// (the dispatch loop already authenticated and authorized the frame via
// s.authorize before calling this handler, so a second authorization pass
// would be redundant), and encodes the returned response message back to
// the frame payload.
func projectGRPCMethod(fullMethod string, impl any, md grpc.MethodDesc) methodHandler {
	return func(ctx context.Context, payload []byte) ([]byte, error) {
		dec := func(v any) error {
			msg, ok := v.(proto.Message)
			if !ok {
				return &ModuleMethodRequestTypeError{Method: fullMethod}
			}
			if err := proto.Unmarshal(payload, msg); err != nil {
				return &UnmarshalError{Err: err}
			}
			return nil
		}

		out, err := md.Handler(impl, ctx, dec, nil)
		if err != nil {
			return nil, err
		}

		msg, ok := out.(proto.Message)
		if !ok {
			return nil, &ModuleMethodResponseTypeError{Method: fullMethod}
		}

		resp, err := proto.Marshal(msg)
		if err != nil {
			return nil, &MarshalError{Err: err}
		}
		return resp, nil
	}
}
