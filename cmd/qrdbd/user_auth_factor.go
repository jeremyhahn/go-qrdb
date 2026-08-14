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

// user_auth_factor.go wires the M14 user-side auth-factor service
// (service.UserAuthFactorServicer, docs/08-bootstrap-auth-architecture.md
// §5e) into the daemon's multi-protocol server. This mirrors, construction
// shape for construction shape, how sdk/go/node.go's buildLoginOpts wires the
// same service into the SDK's embedded transport: a
// service.UserAuthFactorServiceConfig carrying only the KV servicer (no
// UserStateUpdater). Doing so here gives "qrdb auth login" / "qrdb auth
// factor add|list|rotate|relabel|remove" a working surface against a
// standalone qrdbd daemon identical to what they already have against an
// embedded node.
package main

import (
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// buildUserAuthFactorServiceOption constructs the M14 user-side auth-factor
// service backed by kv and returns the server.ServerOption that wires it
// into every protocol server (REST, gRPC, QUIC, MCP, Unix) via
// server.WithUserAuthFactorService.
//
// UserStateUpdater is left nil, exactly matching sdk/go/node.go's
// buildLoginOpts (the embedded-transport wiring): force-removing a user's
// last factor still revokes the factor, it just does not flip a
// user-active flag on this path either, keeping the daemon and embedded
// surfaces identical.
//
// Returns an error when kv is nil (service.NewUserAuthFactorService requires
// a KV servicer to persist factor records). Callers should log the error and
// continue booting without the M14 surface rather than aborting startup,
// since it is an additive capability layered on top of the always-present
// user/role management services.
func buildUserAuthFactorServiceOption(kv service.KVServicer) (server.ServerOption, error) {
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{
		KV: kv,
	})
	if err != nil {
		return nil, err
	}
	return server.WithUserAuthFactorService(svc), nil
}
