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

package rest

import (
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// The body-targeted routes' extractors. Each decodes the body into the
// handler's OWN request type, so the route-level check authorizes exactly the
// tenant or user the handler then acts on (see authmw.RESTBody).
var (
	createUserTarget = authmw.RESTBody(func(r *createUserRequest) authmw.Target {
		return authmw.TenantTarget(r.TenantID)
	})
	lookupUserTarget = authmw.RESTBody(func(r *lookupUserRequest) authmw.Target {
		return authmw.TenantTarget(r.TenantID)
	})
	createRoleTarget = authmw.RESTBody(func(r *createRoleRequest) authmw.Target {
		return authmw.TenantTarget(r.TenantID)
	})
	reconcileRolesTarget = authmw.RESTBody(func(r *reconcileRolesRequest) authmw.Target {
		return authmw.TenantTarget(r.TenantID)
	})
	assignRoleTarget = authmw.RESTBody(func(r *assignRoleRequest) authmw.Target {
		return authmw.UserTarget(r.UserID)
	})
)
