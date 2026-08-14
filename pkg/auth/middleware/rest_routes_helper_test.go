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
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// testRESTRoutes builds a RESTRouteRegistry covering the routes the
// middleware tests exercise. It mirrors how the REST server populates the
// registry at route-registration time (pkg/api/rest registerRoutes): each
// route declares its permission (or an explicit public/authenticated
// marker) exactly once.
func testRESTRoutes() *RESTRouteRegistry {
	rr := NewRESTRouteRegistry()

	// Pre-authentication public allowlist mirrors.
	rr.RegisterPublic(http.MethodGet, "/health")
	rr.RegisterPublic(http.MethodGet, "/v1/ca/bootstrap")
	rr.RegisterPublic(http.MethodGet, "/v1/config/initialized")

	// Authenticated-but-unprivileged.
	rr.RegisterAuthenticated(http.MethodGet, "/metrics")

	// Permission-mapped routes used across the middleware tests.
	rr.Register(http.MethodGet, "/status", permission.AdminStatus)
	rr.Register(http.MethodGet, "/kv", permission.KVGet)
	rr.Register(http.MethodPut, "/kv", permission.KVPut)
	rr.Register(http.MethodDelete, "/kv", permission.KVDelete)
	rr.Register(http.MethodGet, "/kv/*", permission.KVGet)
	rr.Register(http.MethodPut, "/kv/*", permission.KVPut)
	rr.Register(http.MethodDelete, "/kv/*", permission.KVDelete)
	rr.Register(http.MethodGet, "/v1/barrier/status", permission.SealStatus)
	rr.Register(http.MethodPost, "/v1/barrier/rekey", permission.SealRekey)
	rr.Register(http.MethodPost, "/v1/ceremony/initialize", permission.CeremonyInitialize)
	rr.Register(http.MethodPost, "/v1/barrier/officers/{username}/claim/begin", permission.IdentityGated)

	return rr
}
