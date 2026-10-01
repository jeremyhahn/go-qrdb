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

// auth_factor.go wires the ceremony-time custodian auth-factor service
// (service.AuthFactorServicer, /v1/authfactors/* — consumers like xkms) into
// the daemon's multi-protocol server. It mirrors, construction shape for
// construction shape, user_auth_factor.go's wiring of the user-side service:
// the TENANT-ROUTED service over the same tenant.StoreResolver the served
// user and role services, and the M14 user auth-factor service, already route
// through. Doing so here closes the gap go-qrdb#464 records: this surface was
// the one place left binding to one flat KV after go-qrdb#393 tenant-routed
// everything else in the same two files.
package main

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ErrNoTenantStoreResolverForAuthFactors is returned when the ceremony-time
// custodian auth-factor surface is requested on a daemon that has no tenant
// store resolver. It is a refusal, not a degradation: the resolver IS the
// tenant boundary a custodian enrolment resolves through, so a service built
// without one resolves every tenant's enrolments out of one keyspace — the
// same defect go-qrdb#393 closed for the user-side service.
var ErrNoTenantStoreResolverForAuthFactors = errors.New("qrdbd: auth factor service requires a tenant store resolver")

// buildAuthFactorServiceOption returns the server.ServerOption that wires the
// tenant-routed ceremony/custodian auth-factor service into every protocol
// server (REST, gRPC, QUIC, MCP, Unix).
//
// There is no legacy-record migration, unlike buildUserAuthFactorServiceOption:
// this repository is pre-release alpha and carries no migration guarantee
// across the cutover (owner ruling, go-qrdb#464). Records a previous build
// wrote at the flat "__sys:authfactor/{id}" key are left exactly where they
// are, inert — a resolved store confines every key under "{tenant}/", so they
// are not reachable through the routed service for any tenant, the system
// tenant included. An operator who needs them re-runs the custodian enrolment
// ceremony. See docs/auth-factors/engineering.md.
//
// Returns an error when no resolver is available or the service construction
// itself failed. The caller logs it and continues booting without this
// surface rather than aborting startup, matching buildUserAuthFactorServiceOption:
// serving it unrouted is the defect, not the fallback.
func buildAuthFactorServiceOption(resolver tenant.StoreResolver, factors []authfactors.Factor) (server.ServerOption, error) {
	if resolver == nil {
		return nil, ErrNoTenantStoreResolverForAuthFactors
	}
	svc, err := service.NewRoutingAuthFactorService(service.RoutingAuthFactorServiceConfig{
		Factors:  factors,
		Resolver: resolver,
	})
	if err != nil {
		return nil, err
	}
	return server.WithAuthFactorService(svc), nil
}
