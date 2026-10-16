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
// (service.UserAuthFactorServicer) into the daemon's multi-protocol server.
// It mirrors, construction shape for construction shape, how the SDK's
// embedded node wires the same service: the TENANT-ROUTED service over the
// same tenant.StoreResolver the served user and role services route through.
// Doing so here gives "qrdb auth login" / "qrdb auth factor
// add|list|rotate|relabel|remove" a working surface against a standalone
// qrdbd daemon identical to what they already have against an embedded node —
// including the tenant boundary (go-qrdb#393).
package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ErrNoTenantStoreResolver is returned when the user-side auth-factor surface
// is requested on a daemon that has no tenant store resolver. It is a refusal,
// not a degradation: the resolver IS the tenant boundary of factor resolution,
// so a service built without one resolves every tenant's credentials out of
// one keyspace, which is the defect go-qrdb#393 closes.
var ErrNoTenantStoreResolver = errors.New("qrdbd: user auth-factor service requires a tenant store resolver")

// buildUserAuthFactorServiceOption migrates any legacy untenanted auth-factor
// records into their owning tenants and returns the server.ServerOption that
// wires the tenant-routed service into every protocol server (REST, gRPC,
// QUIC, MCP, Unix).
//
// UserStateUpdater is left nil, exactly matching the embedded-transport
// wiring: force-removing a user's last factor still revokes the factor, it
// just does not flip a user-active flag on this path either, keeping the
// daemon and embedded surfaces identical.
//
// The legacy store is handed to the service rather than migrated here: the
// service runs the migration once, before it answers anything, which makes
// serving-before-migrating unrepresentable and puts the read at a moment the
// Raft store can serve it. Refusals are logged at ERROR and do not block the
// surface; nothing is ever placed in a guessed tenant. See
// service.NewRoutingUserAuthFactorService.
//
// Returns an error when no resolver is available or when the store itself
// failed. Callers log it and continue booting without the M14 surface rather
// than aborting startup: it is an additive capability layered on top of the
// always-present user/role management services, and serving it unrouted is
// the defect, not the fallback.
func buildUserAuthFactorServiceOption(_ context.Context, resolver tenant.StoreResolver, legacyStore kvstore.KVStore, trail audit.AuditLogger, logger *slog.Logger) (server.ServerOption, error) {
	if resolver == nil {
		return nil, ErrNoTenantStoreResolver
	}
	if logger == nil {
		logger = slog.Default()
	}
	svc, err := service.NewRoutingUserAuthFactorService(service.RoutingUserAuthFactorServiceConfig{
		Resolver:    resolver,
		LegacyStore: legacyStore,
		AuditLogger: trail,
		Logger:      logger,
	})
	if err != nil {
		return nil, err
	}
	return server.WithUserAuthFactorService(svc), nil
}
