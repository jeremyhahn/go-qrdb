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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/tenant"

// ---------------------------------------------------------------------------
// Tenant metadata types
//
// Note: ServiceTenantInfo, ServiceTenantBarrierStatus, and
// ServiceTenantServicer are re-exported in types_service.go. The aliases below
// cover the remaining tenant primitives and use a Tenant-prefix where a plain
// name would collide with another package's symbol in the flat SDK.
// ---------------------------------------------------------------------------

// TenantPatch is a sparse update to a tenant's metadata.
type TenantPatch = tenant.TenantPatch

// TenantIsolationType identifies a tenant's isolation mode. (Aliases
// pkg/tenant.IsolationType.)
type TenantIsolationType = tenant.IsolationType

// TenantIsolationBarrier is the per-tenant barrier isolation mode. (Aliases
// pkg/tenant.IsolationBarrier.)
const TenantIsolationBarrier = tenant.IsolationBarrier

// SystemTenantID is the identifier of the reserved system tenant.
const SystemTenantID = tenant.SystemTenantID

// ErrTenantExists is returned by the tenant store when creating a tenant whose
// id already exists. (Aliases pkg/tenant.ErrTenantAlreadyExists; named
// ErrTenantExists to avoid colliding with the seal package's
// ErrTenantAlreadyExists, which is a distinct sentinel.)
var ErrTenantExists = tenant.ErrTenantAlreadyExists
